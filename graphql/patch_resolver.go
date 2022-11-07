package graphql

// This file will be automatically regenerated based on the schema, any resolver implementations
// will be copied through when generating and any unknown code will be moved to the end.

import (
	"context"
	"fmt"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/build"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/evergreen/model/user"
	"github.com/evergreen-ci/evergreen/rest/data"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/utility"
)

// AuthorDisplayName is the resolver for the authorDisplayName field.
func (r *patchResolver) AuthorDisplayName(ctx context.Context, obj *restModel.APIPatch) (string, error) {
	usr, err := user.FindOneById(*obj.Author)
	if err != nil {
		return "", ResourceNotFound.Send(ctx, fmt.Sprintf("Error getting user from user ID: %s", err.Error()))
	}
	if usr == nil {
		return "", ResourceNotFound.Send(ctx, "Could not find user from user ID")
	}
	return usr.DisplayName(), nil
}

// BaseTaskStatuses is the resolver for the baseTaskStatuses field.
func (r *patchResolver) BaseTaskStatuses(ctx context.Context, obj *restModel.APIPatch) ([]string, error) {
	baseTasks, err := getVersionBaseTasks(*obj.Id)
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error getting version base tasks: %s", err.Error()))
	}
	return getAllTaskStatuses(baseTasks), nil
}

// Builds is the resolver for the builds field.
func (r *patchResolver) Builds(ctx context.Context, obj *restModel.APIPatch) ([]*restModel.APIBuild, error) {
	builds, err := build.FindBuildsByVersions([]string{*obj.Version})
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error finding build by version %s: %s", *obj.Version, err.Error()))
	}
	var apiBuilds []*restModel.APIBuild
	for _, build := range builds {
		apiBuild := restModel.APIBuild{}
		apiBuild.BuildFromService(build)
		apiBuilds = append(apiBuilds, &apiBuild)
	}
	return apiBuilds, nil
}

// CommitQueuePosition is the resolver for the commitQueuePosition field.
func (r *patchResolver) CommitQueuePosition(ctx context.Context, obj *restModel.APIPatch) (*int, error) {
	if err := obj.GetCommitQueuePosition(); err != nil {
		return nil, InternalServerError.Send(ctx, err.Error())
	}
	return obj.CommitQueuePosition, nil
}

// Duration is the resolver for the duration field.
func (r *patchResolver) Duration(ctx context.Context, obj *restModel.APIPatch) (*PatchDuration, error) {
	query := db.Query(task.ByVersion(*obj.Id)).WithFields(task.TimeTakenKey, task.StartTimeKey, task.FinishTimeKey, task.DisplayOnlyKey, task.ExecutionKey)
	tasks, err := task.FindAllFirstExecution(query)
	if err != nil {
		return nil, InternalServerError.Send(ctx, err.Error())
	}
	if tasks == nil {
		return nil, ResourceNotFound.Send(ctx, "Could not find any tasks for patch")
	}
	timeTaken, makespan := task.GetTimeSpent(tasks)

	// return nil if rounded timeTaken/makespan == 0s
	t := timeTaken.Round(time.Second).String()
	var tPointer *string
	if t != "0s" {
		tFormated := formatDuration(t)
		tPointer = &tFormated
	}
	m := makespan.Round(time.Second).String()
	var mPointer *string
	if m != "0s" {
		mFormated := formatDuration(m)
		mPointer = &mFormated
	}

	return &PatchDuration{
		Makespan:  mPointer,
		TimeTaken: tPointer,
	}, nil
}

// PatchTriggerAliases is the resolver for the patchTriggerAliases field.
func (r *patchResolver) PatchTriggerAliases(ctx context.Context, obj *restModel.APIPatch) ([]*restModel.APIPatchTriggerDefinition, error) {
	projectRef, err := data.FindProjectById(*obj.ProjectId, true, true)
	if err != nil || projectRef == nil {
		return nil, ResourceNotFound.Send(ctx, fmt.Sprintf("Could not find project: %s : %s", *obj.ProjectId, err))
	}

	if len(projectRef.PatchTriggerAliases) == 0 {
		return nil, nil
	}

	projectCache := map[string]*model.Project{}
	aliases := []*restModel.APIPatchTriggerDefinition{}
	for _, alias := range projectRef.PatchTriggerAliases {
		project, projectCached := projectCache[alias.ChildProject]
		if !projectCached {
			_, project, err = model.FindLatestVersionWithValidProject(alias.ChildProject)
			if err != nil {
				return nil, InternalServerError.Send(ctx, fmt.Sprintf("getting last known project for '%s': %s", alias.ChildProject, err.Error()))
			}
			projectCache[alias.ChildProject] = project
		}

		matchingTasks, err := project.VariantTasksForSelectors([]patch.PatchTriggerDefinition{alias}, *obj.Requester)
		if err != nil {
			return nil, InternalServerError.Send(ctx, fmt.Sprintf("Problem matching tasks to alias definitions: %v", err.Error()))
		}

		variantsTasks := []restModel.VariantTask{}
		for _, vt := range matchingTasks {
			variantsTasks = append(variantsTasks, restModel.VariantTask{
				Name:  utility.ToStringPtr(vt.Variant),
				Tasks: utility.ToStringPtrSlice(vt.Tasks),
			})
		}

		identifier, err := model.GetIdentifierForProject(alias.ChildProject)
		if err != nil {
			return nil, InternalServerError.Send(ctx, fmt.Sprintf("Problem getting child project identifier: %v", err.Error()))
		}

		aliases = append(aliases, &restModel.APIPatchTriggerDefinition{
			Alias:                  utility.ToStringPtr(alias.Alias),
			ChildProjectId:         utility.ToStringPtr(alias.ChildProject),
			ChildProjectIdentifier: utility.ToStringPtr(identifier),
			VariantsTasks:          variantsTasks,
		})
	}

	return aliases, nil
}

// Project is the resolver for the project field.
func (r *patchResolver) Project(ctx context.Context, obj *restModel.APIPatch) (*PatchProject, error) {
	patchProject, err := getPatchProjectVariantsAndTasksForUI(ctx, obj)
	if err != nil {
		return nil, err
	}
	return patchProject, nil
}

// ProjectIdentifier is the resolver for the projectIdentifier field.
func (r *patchResolver) ProjectIdentifier(ctx context.Context, obj *restModel.APIPatch) (string, error) {
	obj.GetIdentifier()
	return utility.FromStringPtr(obj.ProjectIdentifier), nil
}

// TaskCount is the resolver for the taskCount field.
func (r *patchResolver) TaskCount(ctx context.Context, obj *restModel.APIPatch) (*int, error) {
	taskCount, err := task.Count(db.Query(task.DisplayTasksByVersion(*obj.Id)))
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error getting task count for patch %s: %s", *obj.Id, err.Error()))
	}
	return &taskCount, nil
}

// TaskStatuses is the resolver for the taskStatuses field.
func (r *patchResolver) TaskStatuses(ctx context.Context, obj *restModel.APIPatch) ([]string, error) {
	defaultSort := []task.TasksSortOrder{
		{Key: task.DisplayNameKey, Order: 1},
	}
	opts := task.GetTasksByVersionOptions{
		Sorts:                          defaultSort,
		IncludeBaseTasks:               false,
		IncludeBuildVariantDisplayName: false,
		IsPatch:                        evergreen.IsPatchRequester(utility.FromStringPtr(obj.Requester)),
	}
	tasks, _, err := task.GetTasksByVersion(*obj.Id, opts)
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error getting version tasks: %s", err.Error()))
	}
	return getAllTaskStatuses(tasks), nil
}

// Time is the resolver for the time field.
func (r *patchResolver) Time(ctx context.Context, obj *restModel.APIPatch) (*PatchTime, error) {
	usr := mustHaveUser(ctx)

	started, err := getFormattedDate(obj.StartTime, usr.Settings.Timezone)
	if err != nil {
		return nil, InternalServerError.Send(ctx, err.Error())
	}
	finished, err := getFormattedDate(obj.FinishTime, usr.Settings.Timezone)
	if err != nil {
		return nil, InternalServerError.Send(ctx, err.Error())
	}
	submittedAt, err := getFormattedDate(obj.CreateTime, usr.Settings.Timezone)
	if err != nil {
		return nil, InternalServerError.Send(ctx, err.Error())
	}

	return &PatchTime{
		Started:     started,
		Finished:    finished,
		SubmittedAt: *submittedAt,
	}, nil
}

// VersionFull is the resolver for the versionFull field.
func (r *patchResolver) VersionFull(ctx context.Context, obj *restModel.APIPatch) (*restModel.APIVersion, error) {
	if utility.FromStringPtr(obj.Version) == "" {
		return nil, nil
	}
	v, err := model.VersionFindOneId(*obj.Version)
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("Error while finding version with id: `%s`: %s", *obj.Version, err.Error()))
	}
	if v == nil {
		return nil, ResourceNotFound.Send(ctx, fmt.Sprintf("Unable to find version with id: `%s`", *obj.Version))
	}
	apiVersion := restModel.APIVersion{}
	apiVersion.BuildFromService(*v)
	return &apiVersion, nil
}

// Patch returns PatchResolver implementation.
func (r *Resolver) Patch() PatchResolver { return &patchResolver{r} }

type patchResolver struct{ *Resolver }
