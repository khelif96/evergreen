package event

import (
	"testing"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/smartystreets/goconvey/convey/reporting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

func init() {
	reporting.QuietMode()

}

func TestLoggingTaskEvents(t *testing.T) {
	Convey("Test task event logging", t, func() {

		require.NoError(t, db.Clear(EventCollection))

		Convey("All task events should be logged correctly", func() {
			taskId := "task_id"
			hostId := "host_id"

			LogTaskCreated(t.Context(), taskId, 1)
			time.Sleep(1 * time.Millisecond)
			LogHostTaskDispatched(t.Context(), taskId, 1, hostId)
			time.Sleep(1 * time.Millisecond)
			LogHostTaskUndispatched(t.Context(), taskId, 1, hostId)
			time.Sleep(1 * time.Millisecond)
			LogTaskStarted(t.Context(), taskId, 1)
			time.Sleep(1 * time.Millisecond)
			LogHostTaskFinished(t.Context(), taskId, 1, hostId, evergreen.TaskSucceeded)

			eventsForTask, err := Find(t.Context(), TaskEventsInOrder(taskId))
			So(err, ShouldEqual, nil)

			event := eventsForTask[0]
			So(TaskCreated, ShouldEqual, event.EventType)
			So(taskId, ShouldEqual, event.ResourceId)

			eventData, ok := event.Data.(*TaskEventData)
			So(ok, ShouldBeTrue)
			So(event.ResourceType, ShouldEqual, ResourceTypeTask)
			So(eventData.Execution, ShouldEqual, 1)
			So(eventData.HostId, ShouldBeBlank)
			So(eventData.UserId, ShouldBeBlank)
			So(eventData.Status, ShouldBeBlank)
			So(eventData.Timestamp.IsZero(), ShouldBeTrue)

			event = eventsForTask[1]
			So(TaskDispatched, ShouldEqual, event.EventType)
			So(taskId, ShouldEqual, event.ResourceId)

			eventData, ok = event.Data.(*TaskEventData)
			So(ok, ShouldBeTrue)
			So(event.ResourceType, ShouldEqual, ResourceTypeTask)
			So(eventData.Execution, ShouldEqual, 1)
			So(eventData.HostId, ShouldEqual, hostId)
			So(eventData.UserId, ShouldBeBlank)
			So(eventData.Status, ShouldBeBlank)
			So(eventData.Timestamp.IsZero(), ShouldBeTrue)

			event = eventsForTask[2]
			So(TaskUndispatched, ShouldEqual, event.EventType)
			So(taskId, ShouldEqual, event.ResourceId)

			eventData, ok = event.Data.(*TaskEventData)
			So(ok, ShouldBeTrue)
			So(event.ResourceType, ShouldEqual, ResourceTypeTask)
			So(eventData.Execution, ShouldEqual, 1)
			So(eventData.HostId, ShouldEqual, hostId)
			So(eventData.UserId, ShouldBeBlank)
			So(eventData.Status, ShouldBeBlank)
			So(eventData.Timestamp.IsZero(), ShouldBeTrue)

			event = eventsForTask[3]
			So(TaskStarted, ShouldEqual, event.EventType)
			So(taskId, ShouldEqual, event.ResourceId)

			eventData, ok = event.Data.(*TaskEventData)
			So(ok, ShouldBeTrue)
			So(event.ResourceType, ShouldEqual, ResourceTypeTask)
			So(eventData.Execution, ShouldEqual, 1)
			So(eventData.HostId, ShouldBeBlank)
			So(eventData.UserId, ShouldBeBlank)
			So(eventData.Status, ShouldEqual, evergreen.TaskStarted)
			So(eventData.Timestamp.IsZero(), ShouldBeTrue)

			event = eventsForTask[4]
			So(TaskFinished, ShouldEqual, event.EventType)
			So(taskId, ShouldEqual, event.ResourceId)

			eventData, ok = event.Data.(*TaskEventData)
			So(ok, ShouldBeTrue)
			So(event.ResourceType, ShouldEqual, ResourceTypeTask)
			So(eventData.Execution, ShouldEqual, 1)
			So(eventData.HostId, ShouldBeBlank)
			So(eventData.UserId, ShouldBeBlank)
			So(eventData.Status, ShouldEqual, evergreen.TaskSucceeded)
			So(eventData.Timestamp.IsZero(), ShouldBeTrue)
		})
	})
}

func TestLogManyTestEvents(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	require.NoError(db.ClearCollections(EventCollection))
	LogManyTaskAbortRequests(t.Context(), []string{"task_1", "task_2"}, "example_user")
	events := []EventLogEntry{}
	assert.NoError(db.FindAllQ(t.Context(), EventCollection, db.Query(bson.M{}), &events))
	assert.Len(events, 2)
}
