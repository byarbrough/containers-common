package libimage

import (
	"context"
	"fmt"
	"strings"
	"time"

	dockerTransport "github.com/containers/image/v5/docker"
	dockerArchiveTransport "github.com/containers/image/v5/docker/archive"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/sirupsen/logrus"
)

// PushOptions allows for custommizing image pushes.
type PushOptions struct {
	CopyOptions
	AllTags bool
}

// Push pushes the specified source which must refer to an image in the local
// containers storage.  It may or may not have the `containers-storage:`
// prefix.  Use destination to push to a custom destination.  The destination
// can refer to any supported transport.  If not transport is specified, the
// docker transport (i.e., a registry) is implied.  If destination is left
// empty, the docker destination will be extrapolated from the source.
//
// Return storage.ErrImageUnknown if source could not be found in the local
// containers storage.
func (r *Runtime) Push(ctx context.Context, source, destination string, options *PushOptions) ([]byte, error) {
	if options == nil {
		options = &PushOptions{}
	}

	// Look up the local image.  Note that we need to ignore the platform
	// and push what the user specified (containers/podman/issues/10344).
	image, resolvedSource, err := r.LookupImage(source, nil)
	if err != nil {
		return nil, err
	}

	// Make sure we have a proper destination, and parse it into an image
	// reference for copying.
	if destination == "" {
		// Doing an ID check here is tempting but false positives (due
		// to a short partial IDs) are more painful than false
		// negatives.
		destination = resolvedSource
	}

	// If specified to push --all-tags, look them up and iterate.
	if options.AllTags {

		// Do not allow : for tags, other than specifying transport
		d := strings.TrimPrefix(destination, "docker://")
		if strings.ContainsAny(d, ":") {
			return nil, fmt.Errorf("tag can't be used with --all-tags/-a")
		}

		namedRepoTags, err := image.NamedTaggedRepoTags()
		if err != nil {
			return nil, err
		}

		logrus.Debugf("Flag --all-tags true, found: %s", namedRepoTags)

		for _, tag := range namedRepoTags {
			fullNamedTag := fmt.Sprintf("%s:%s", destination, tag.Tag())
			_, err = pushImage(ctx, fullNamedTag, options, image, r)
			if err != nil {
				return nil, err
			}
		}
	} else {
		// No --all-tags, so just push just the single image.
		return pushImage(ctx, destination, options, image, r)
	}

	return nil, nil
}

func pushImage(ctx context.Context, destination string, options *PushOptions, image *Image, r *Runtime) ([]byte, error) {
	srcRef, err := image.StorageReference()
	if err != nil {
		return nil, err
	}

	logrus.Debugf("Pushing image %s to %s", srcRef, destination)

	destRef, err := alltransports.ParseImageName(destination)
	if err != nil {
		// If the input does not include a transport assume it refers
		// to a registry.
		dockerRef, dockerErr := alltransports.ParseImageName("docker://" + destination)
		if dockerErr != nil {
			return nil, err
		}
		destRef = dockerRef
	}

	// If using --all-tags, must push to registry
	if destRef.Transport().Name() != dockerTransport.Transport.Name() && options.AllTags {
		return nil, fmt.Errorf("--all-tags can only be used with docker transport")
	}

	if r.eventChannel != nil {
		defer r.writeEvent(&Event{ID: image.ID(), Name: destination, Time: time.Now(), Type: EventTypeImagePush})
	}

	// Buildah compat: Make sure to tag the destination image if it's a
	// Docker archive. This way, we preserve the image name.
	if destRef.Transport().Name() == dockerArchiveTransport.Transport.Name() {
		if named, err := reference.ParseNamed(destination); err == nil {
			tagged, isTagged := named.(reference.NamedTagged)
			if isTagged {
				options.dockerArchiveAdditionalTags = []reference.NamedTagged{tagged}
			}
		}
	}

	c, err := r.newCopier(&options.CopyOptions)
	if err != nil {
		return nil, err
	}

	defer c.close()

	return c.copy(ctx, srcRef, destRef)
}
