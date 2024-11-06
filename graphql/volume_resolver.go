package graphql

import (
	"context"
	"fmt"

	"github.com/evergreen-ci/evergreen/model/host"
	restModel "github.com/evergreen-ci/evergreen/rest/model"
	"github.com/evergreen-ci/utility"
)

// Host is the resolver for the host field.
func (r *volumeResolver) Host(ctx context.Context, obj *restModel.APIVolume) (*restModel.APIHost, error) {
	if obj.HostID == nil || *obj.HostID == "" {
		return nil, nil
	}
	h, err := host.FindOneId(ctx, *obj.HostID)
	if err != nil {
		return nil, InternalServerError.Send(ctx, fmt.Sprintf("finding host '%s': %s", utility.FromStringPtr(obj.HostID), err.Error()))
	}
	if h == nil {
		return nil, ResourceNotFound.Send(ctx, fmt.Sprintf("Unable to find host %s", *obj.HostID))
	}
	apiHost := restModel.APIHost{}
	apiHost.BuildFromService(h, nil)
	return &apiHost, nil
}

// Volume returns VolumeResolver implementation.
func (r *Resolver) Volume() VolumeResolver { return &volumeResolver{r} }

type volumeResolver struct{ *Resolver }
