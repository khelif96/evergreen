package data

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	mgobson "github.com/evergreen-ci/evergreen/db/mgo/bson"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/taskstats"
	"github.com/evergreen-ci/utility"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockGetTaskStats(t *testing.T) {
	defer func() {
		assert.NoError(t, db.ClearCollections(taskstats.DailyTaskStatsCollection, model.ProjectRefCollection))
	}()
	assert.NoError(t, db.ClearCollections(taskstats.DailyTaskStatsCollection, model.ProjectRefCollection))

	proj := model.ProjectRef{
		Id: "project",
	}
	require.NoError(t, proj.Insert(t.Context()))

	// Add stats
	filter := &taskstats.StatsFilter{}
	assert.NoError(t, insertTaskStats(t.Context(), filter, 102, 100))

	stats, err := GetTaskStats(t.Context(), *filter)
	assert.NoError(t, err)
	assert.Len(t, stats, 100)

	assert.Equal(t, fmt.Sprintf("task_%v", 0), *stats[0].TaskName)

}

func TestGetTaskStats(t *testing.T) {
	defer func() {
		assert.NoError(t, db.ClearCollections(taskstats.DailyTaskStatsCollection, model.ProjectRefCollection))
	}()
	assert.NoError(t, db.ClearCollections(taskstats.DailyTaskStatsCollection, model.ProjectRefCollection))

	proj := model.ProjectRef{
		Id: "project",
	}
	require.NoError(t, proj.Insert(t.Context()))

	stat := taskstats.DBTaskStats{
		Id: taskstats.DBTaskStatsID{
			Project:   "projectID",
			TaskName:  "t0",
			Date:      time.Date(2022, 02, 15, 0, 0, 0, 0, time.UTC),
			Requester: evergreen.RepotrackerVersionRequester,
		},
	}
	assert.NoError(t, db.Insert(t.Context(), taskstats.DailyTaskStatsCollection, stat))
	projectRef := model.ProjectRef{
		Id:         "projectID",
		Identifier: "projectName",
	}
	assert.NoError(t, projectRef.Insert(t.Context()))

	stats, err := GetTaskStats(t.Context(), taskstats.StatsFilter{
		Project:      "projectName",
		GroupNumDays: 1,
		Requesters:   []string{evergreen.RepotrackerVersionRequester},
		Sort:         taskstats.SortLatestFirst,
		GroupBy:      taskstats.GroupByTask,
		AfterDate:    time.Time{},
		BeforeDate:   time.Date(2022, 02, 16, 0, 0, 0, 0, time.UTC),
		Limit:        1,
		Tasks:        []string{"t0"},
	})
	assert.NoError(t, err)
	require.Len(t, stats, 1)
}

func insertTaskStats(ctx context.Context, filter *taskstats.StatsFilter, numTests int, limit int) error {
	day := time.Now()
	tasks := []string{}
	for i := 0; i < numTests; i++ {
		taskName := fmt.Sprintf("%v%v", "task_", i)
		tasks = append(tasks, taskName)
		err := db.Insert(ctx, taskstats.DailyTaskStatsCollection, mgobson.M{
			"_id": taskstats.DBTaskStatsID{
				Project:      "project",
				Requester:    "requester",
				TaskName:     taskName,
				BuildVariant: "variant",
				Distro:       "distro",
				Date:         utility.GetUTCDay(day),
			},
		})
		if err != nil {
			return err
		}
	}
	*filter = taskstats.StatsFilter{
		Limit:        limit,
		Project:      "project",
		Requesters:   []string{"requester"},
		Tasks:        tasks,
		GroupBy:      "distro",
		GroupNumDays: 1,
		Sort:         taskstats.SortEarliestFirst,
		BeforeDate:   utility.GetUTCDay(time.Now().Add(dayInHours)),
		AfterDate:    utility.GetUTCDay(time.Now().Add(-dayInHours)),
	}
	return nil
}
