package units

import (
	"context"
	"fmt"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model/alertrecord"
	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/model/host"
	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/registry"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/mongodb/grip/sometimes"
	"github.com/pkg/errors"
)

const (
	volumeExpirationWarningsName = "volume-expiration-warnings"
)

func init() {
	registry.AddJobType(volumeExpirationWarningsName,
		func() amboy.Job { return makeVolumeExpirationWarningsJob() })
}

type volumeExpirationWarningsJob struct {
	job.Base `bson:"job_base" json:"job_base" yaml:"job_base"`
}

func makeVolumeExpirationWarningsJob() *volumeExpirationWarningsJob {
	j := &volumeExpirationWarningsJob{
		Base: job.Base{
			JobType: amboy.JobType{
				Name:    volumeExpirationWarningsName,
				Version: 0,
			},
		},
	}
	return j
}

func NewVolumeExpirationWarningsJob(id string) amboy.Job {
	j := makeVolumeExpirationWarningsJob()
	j.SetID(fmt.Sprintf("%s.%s", volumeExpirationWarningsName, id))
	j.SetScopes([]string{volumeExpirationWarningsName})
	j.SetEnqueueAllScopes(true)
	return j
}

func (j *volumeExpirationWarningsJob) Run(ctx context.Context) {
	defer j.MarkComplete()

	flags, err := evergreen.GetServiceFlags(ctx)
	if err != nil {
		j.AddError(errors.Wrap(err, "getting service flags"))
		return
	}
	if flags.AlertsDisabled {
		grip.InfoWhen(sometimes.Percent(evergreen.DegradedLoggingPercent), message.Fields{
			"runner":  "alerter",
			"id":      j.ID(),
			"message": "alerts are disabled, exiting",
		})
		return
	}

	// Do alerts for volumes - collect all volumes that are unattached.
	// The trigger logic will filter out any volumes that aren't in a notification window, or have
	// already have alerts sent.
	unattachedVolumes, err := host.FindUnattachedExpirableVolumes(ctx)
	if err != nil {
		j.AddError(errors.WithStack(err))
		return
	}

	for _, v := range unattachedVolumes {
		if ctx.Err() != nil {
			j.AddError(errors.Wrap(ctx.Err(), "volume expiration warning run canceled"))
			return
		}
		if err = runVolumeWarningTriggers(ctx, v); err != nil {
			j.AddError(err)
			grip.Error(message.WrapError(err, message.Fields{
				"runner":    "monitor",
				"id":        j.ID(),
				"message":   "error queueing volume expiration warning alert",
				"volume_id": v.ID,
			}))
		}
	}
}

func runVolumeWarningTriggers(ctx context.Context, v host.Volume) error {
	catcher := grip.NewSimpleCatcher()
	// try notifying with the largest notification types first.
	// If an alert has been sent, don't continue to the smaller types.
	triggerHours := []int{24 * 21, 24 * 14, 24 * 7, 12, 2}
	for _, numHours := range triggerHours {
		ok, err := tryVolumeNotification(ctx, v, numHours)
		catcher.Add(errors.Wrapf(err, "trying to send volume expiration warning notification"))
		if ok {
			return catcher.Resolve()
		}
	}
	return catcher.Resolve()
}

// return true if a notification was added
func tryVolumeNotification(ctx context.Context, v host.Volume, numHours int) (bool, error) {
	shouldExec, err := shouldNotifyForVolumeExpiration(ctx, v, numHours)
	if err != nil {
		return false, err
	}
	if !shouldExec {
		return false, nil
	}
	event.LogVolumeExpirationWarningSent(ctx, v.ID)
	grip.Info(message.Fields{
		"message":    "sent volume expiration warning",
		"volume_id":  v.ID,
		"host_id":    v.Host,
		"owner":      v.CreatedBy,
		"expiration": v.Expiration,
	})
	if err = alertrecord.InsertNewVolumeExpirationRecord(ctx, v.ID, numHours); err != nil {
		return false, err
	}
	return true, nil
}

func shouldNotifyForVolumeExpiration(ctx context.Context, v host.Volume, numHours int) (bool, error) {
	numHoursLeft := time.Until(v.Expiration)
	// say we have 15 hours left. if 15 > 12, quit. if 15 > 2,  quit.
	// say we have 10 hours left. 10 > 12 nope, so send. 12 > 2 so quit.
	// say we have 30 days left. greater than everything, send no notices.
	// say we have 5 days left. greater than 14 days and 21 days.
	if numHoursLeft > (time.Duration(numHours) * time.Hour) {
		return false, nil
	}
	rec, err := alertrecord.FindByVolumeExpirationWithHours(ctx, v.ID, numHours)
	if err != nil {
		return false, err
	}
	if rec == nil {
		return true, err
	}

	return time.Since(rec.AlertTime) > hostRenotificationInterval, nil
}
