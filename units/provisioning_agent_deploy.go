package units

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/model/event"
	"github.com/evergreen-ci/evergreen/model/host"
	"github.com/evergreen-ci/utility"
	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/dependency"
	"github.com/mongodb/amboy/job"
	"github.com/mongodb/amboy/registry"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
)

const (
	agentDeployJobName = "agent-deploy"
	agentPutRetries    = 75
)

func init() {
	registry.AddJobType(agentDeployJobName, func() amboy.Job {
		return makeAgentDeployJob()
	})
}

type agentDeployJob struct {
	HostID   string `bson:"host_id" json:"host_id" yaml:"host_id"`
	job.Base `bson:"metadata" json:"metadata" yaml:"metadata"`

	host *host.Host
	env  evergreen.Environment
}

func makeAgentDeployJob() *agentDeployJob {
	j := &agentDeployJob{
		Base: job.Base{
			JobType: amboy.JobType{
				Name:    agentDeployJobName,
				Version: 0,
			},
		},
	}

	j.SetDependency(dependency.NewAlways())
	return j
}

func NewAgentDeployJob(env evergreen.Environment, h host.Host, id string) amboy.Job {
	j := makeAgentDeployJob()
	j.host = &h
	j.HostID = h.Id
	j.env = env
	j.SetPriority(1)
	j.SetScopes([]string{fmt.Sprintf("%s.%s", agentDeployJobName, j.HostID)})
	j.SetShouldApplyScopesOnEnqueue(true)
	j.UpdateRetryInfo(amboy.JobRetryOptions{
		Retryable:   utility.TruePtr(),
		MaxAttempts: utility.ToIntPtr(agentPutRetries),
		WaitUntil:   utility.ToTimeDurationPtr(15 * time.Second),
	})
	j.SetID(fmt.Sprintf("%s.%s.%s", agentDeployJobName, j.HostID, id))

	return j
}

func (j *agentDeployJob) Run(ctx context.Context) {
	defer j.MarkComplete()

	flags, err := evergreen.GetServiceFlags()
	if err != nil {
		j.AddRetryableError(err)
		return
	}

	if flags.AgentStartDisabled {
		grip.Debug(message.Fields{
			"mode":     "degraded",
			"host_id":  j.HostID,
			"job":      j.ID(),
			"job_type": j.Type().Name,
		})
		return
	}

	if j.host == nil {
		j.host, err = host.FindOneId(j.HostID)
		if err != nil {
			j.AddError(err)
			return
		}
		if j.host == nil {
			j.AddError(fmt.Errorf("could not find host %s for job %s", j.HostID, j.TaskID))
			return
		}
	}
	if utility.StringSliceContains(evergreen.DownHostStatus, j.host.Status) {
		grip.Debug(message.Fields{
			"host_id": j.host.Id,
			"status":  j.host.Status,
			"message": "host already down, not attempting to deploy agent",
		})
		return
	}

	if !j.host.NeedsNewAgent {
		return
	}

	if j.env == nil {
		j.env = evergreen.GetEnvironment()
	}

	settings := j.env.Settings()

	if err = j.host.UpdateLastCommunicated(); err != nil {
		j.AddRetryableError(errors.Wrapf(err, "error setting LCT on host %s", j.host.Id))
	}

	defer func() {
		if !j.HasErrors() {
			return
		}
		if j.host.Status != evergreen.HostRunning {
			return
		}
		if j.RetryInfo().GetRemainingAttempts() > 0 {
			return
		}

		var externallyTerminated bool
		externallyTerminated, err = handleExternallyTerminatedHost(ctx, j.ID(), j.env, j.host)
		j.AddError(errors.Wrapf(err, "can't check if host '%s' was externally terminated", j.HostID))
		if externallyTerminated {
			return
		}

		if disableErr := HandlePoisonedHost(ctx, j.env, j.host, fmt.Sprintf("failed %d times to put agent on host", agentPutRetries)); disableErr != nil {
			j.AddError(errors.Wrapf(disableErr, "error terminating host %s", j.host.Id))
		}
	}()

	if err := j.startAgentOnHost(ctx, settings); err != nil {
		if j.host.Status == evergreen.HostRunning {
			j.AddRetryableError(err)
		} else {
			j.AddError(err)
		}
		return
	}

	j.AddError(j.host.SetNeedsNewAgent(false))
}

func (j *agentDeployJob) getHostMessage() message.Fields {
	m := message.Fields{
		"message":  "starting agent on host",
		"host_id":  j.host.Host,
		"distro":   j.host.Distro.Id,
		"provider": j.host.Distro.Provider,
	}

	if j.host.InstanceType != "" {
		m["instance"] = j.host.InstanceType
	}

	sinceLCT := time.Since(j.host.LastCommunicationTime)
	if j.host.NeedsNewAgent {
		m["reason"] = "flagged for new agent"
	} else if j.host.LastCommunicationTime.IsZero() {
		m["reason"] = "new host"
	} else if sinceLCT > host.MaxLCTInterval {
		m["reason"] = "host has exceeded last communication threshold"
		m["threshold"] = host.MaxLCTInterval
		m["threshold_span"] = host.MaxLCTInterval.String()
		m["last_communication_at"] = sinceLCT
		m["last_communication_at_time"] = sinceLCT.String()
	}

	return m
}

// Start an agent on the host specified.  First runs any necessary
// preparation on the remote machine, then kicks off the agent process on the
// machine. Returns an error if any step along the way fails.
func (j *agentDeployJob) startAgentOnHost(ctx context.Context, settings *evergreen.Settings) error {
	if err := j.prepRemoteHost(ctx, settings); err != nil {
		return errors.Wrap(err, "could not prep remote host")
	}

	grip.Info(message.Fields{
		"message": "prepping host finished successfully",
		"host_id": j.host.Id,
		"job":     j.ID(),
	})

	// generate the host secret if none exists
	if j.host.Secret == "" {
		if err := j.host.CreateSecret(); err != nil {
			return errors.Wrapf(err, "creating secret for host '%s'", j.host.Id)
		}
	}

	// Start agent to listen for tasks
	grip.Info(j.getHostMessage())
	if logs, err := j.startAgentOnRemote(ctx, settings); err != nil {
		event.LogHostAgentDeployFailed(j.host.Id, err)
		grip.Info(message.WrapError(err, message.Fields{
			"message": "error starting agent on remote",
			"logs":    logs,
			"host_id": j.HostID,
			"job":     j.ID(),
		}))
		return errors.Wrap(err, "could not start agent on remote")
	}
	grip.Info(message.Fields{
		"message": "agent successfully started for host",
		"host_id": j.host.Id,
		"job":     j.ID(),
	})

	if err := j.host.SetAgentRevision(evergreen.AgentVersion); err != nil {
		return errors.Wrapf(err, "error setting agent revision on host %s", j.host.Id)
	}
	return nil
}

const (
	// The app server stops an attempt to curl the evergreen binary after a minute.
	evergreenCurlTimeout = 61 * time.Second
	// sshTimeout defines the timeout for starting the agent.
	startAgentTimeout = 25 * time.Second
)

// Prepare the remote machine to run a task.
func (j *agentDeployJob) prepRemoteHost(ctx context.Context, settings *evergreen.Settings) error {
	// copy over the correct agent binary to the remote host
	curlCtx, cancel := context.WithTimeout(ctx, evergreenCurlTimeout)
	defer cancel()
	curlCmd, err := j.host.CurlCommand(settings)
	if err != nil {
		return errors.Wrap(err, "could not create command to curl evergreen client")
	}
	output, err := j.host.RunSSHCommand(curlCtx, curlCmd)
	if err != nil {
		event.LogHostAgentDeployFailed(j.host.Id, err)
		return errors.Wrapf(err, "error downloading agent binary on remote host: %s", output)
	}
	if curlCtx.Err() != nil {
		return errors.Wrap(curlCtx.Err(), "timed out curling evergreen binary")
	}

	if j.host.Distro.Setup == "" {
		return nil
	}

	if output, err = j.host.RunSSHCommand(ctx, j.host.SetupCommand()); err != nil {
		event.LogHostProvisionFailed(j.host.Id, output)

		grip.Error(message.WrapError(err, message.Fields{
			"message": "error running setup script",
			"host_id": j.host.Id,
			"distro":  j.host.Distro.Id,
			"logs":    output,
			"job":     j.ID(),
		}))

		// there is no guarantee setup scripts are idempotent, so we terminate the host if the setup script fails
		return errors.Wrapf(HandlePoisonedHost(ctx, j.env, j.host, fmt.Sprintf("failed %d times to put agent on host", agentPutRetries)), "error terminating host %s", j.host.Id)
	}

	return nil
}

// Start the agent process on the specified remote host.
func (j *agentDeployJob) startAgentOnRemote(ctx context.Context, settings *evergreen.Settings) (string, error) {
	// build the command to run on the remote machine
	remoteCmd := strings.Join(j.host.AgentCommand(settings, ""), " ")
	grip.Info(message.Fields{
		"message": "starting agent on host",
		"host_id": j.host.Id,
		"command": remoteCmd,
	})

	ctx, cancel := context.WithTimeout(ctx, startAgentTimeout)
	defer cancel()

	startAgentCmd := fmt.Sprintf("nohup %s > /tmp/start 2>&1 &", remoteCmd)
	logs, err := j.host.RunSSHCommand(ctx, startAgentCmd)
	if err != nil {
		return logs, errors.Wrapf(err, "error starting agent on host '%s'", j.host.Id)
	}

	event.LogHostAgentDeployed(j.host.Id)

	return logs, nil
}
