package main

// We want to replace much of this with use of containerd tools
// and also using the Docker API not shelling out

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"golang.org/x/net/context"
)

func dockerRunInput(input io.Reader, image string, args ...string) ([]byte, error) {
	log.Debugf("docker run (input): %s %s", image, strings.Join(args, " "))

	cli, err := dockerClient()
	if err != nil {
		return []byte{}, errors.New("could not initialize Docker API client")
	}

	var resp container.ContainerCreateCreatedBody
	resp, err = cli.ContainerCreate(context.Background(), &container.Config{
		Image:        image,
		Cmd:          args,
		AttachStdout: true,
		AttachStdin:  true,
	}, &container.HostConfig{
		AutoRemove: true,
		LogConfig:  container.LogConfig{Type: "none"},
	}, nil, "")
	if err != nil {
		// if the image wasn't found, try to do a pull and another create
		if client.IsErrNotFound(err) {
			if err := dockerPull(image, false); err != nil {
				return []byte{}, err
			}
			resp, err = cli.ContainerCreate(context.Background(), &container.Config{
				Image:       image,
				Cmd:         args,
				AttachStdin: true,
			}, &container.HostConfig{
				AutoRemove: true,
				LogConfig:  container.LogConfig{Type: "none"},
			}, nil, "")
			// if we error again, bail
			if err != nil {
				return []byte{}, err
			}
		}
		return []byte{}, err
	}

	hijackedResp, err := cli.ContainerAttach(context.Background(), resp.ID, types.ContainerAttachOptions{
		Stdin:  true,
		Stdout: true,
		Stream: true,
	})
	if err != nil {
		return []byte{}, err
	}
	defer hijackedResp.Close()

	if err := cli.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{}); err != nil {
		return []byte{}, err
	}

	if _, err := io.Copy(hijackedResp.Conn, input); err != nil {
		return []byte{}, err
	}

	if _, err = cli.ContainerWait(context.Background(), resp.ID); err != nil {
		return []byte{}, err
	}

	out, err := ioutil.ReadAll(hijackedResp.Reader)
	if err != nil {
		return []byte{}, err
	}
	log.Debugf("docker run (input): %s...Done", strings.Join(args, " "))
	return out, nil
}

func dockerCreate(image string) (string, error) {
	log.Debugf("docker create: %s", image)
	cli, err := dockerClient()
	if err != nil {
		return "", errors.New("could not initialize Docker API client")
	}
	// we do not ever run the container, so /dev/null is used as command
	config := &container.Config{
		Cmd:   []string{"/dev/null"},
		Image: image,
	}
	respBody, err := cli.ContainerCreate(context.Background(), config, nil, nil, "")
	if err != nil {
		return "", err
	}

	log.Debugf("docker create: %s...Done", image)
	return respBody.ID, nil
}

func dockerExport(container string) ([]byte, error) {
	log.Debugf("docker export: %s", container)
	cli, err := dockerClient()
	if err != nil {
		return []byte{}, errors.New("could not initialize Docker API client")
	}
	responseBody, err := cli.ContainerExport(context.Background(), container)
	if err != nil {
		return []byte{}, err
	}
	defer responseBody.Close()

	output := bytes.NewBuffer(nil)
	_, err = io.Copy(output, responseBody)
	if err != nil {
		return []byte{}, err
	}

	return output.Bytes(), nil
}

func dockerRm(container string) error {
	log.Debugf("docker rm: %s", container)
	cli, err := dockerClient()
	if err != nil {
		return errors.New("could not initialize Docker API client")
	}
	if err = cli.ContainerRemove(context.Background(), container, types.ContainerRemoveOptions{}); err != nil {
		return err
	}
	log.Debugf("docker rm: %s...Done", container)
	return nil
}

func dockerPull(image string, trustedPull bool) error {
	log.Debugf("docker pull: %s", image)
	if trustedPull {
		log.Debugf("pulling %s with content trust", image)
		trustedImg, err := TrustedReference(image)
		if err != nil {
			return fmt.Errorf("Trusted pull for %s failed: %v", image, err)
		}
		image = trustedImg.String()
	}
	cli, err := dockerClient()
	if err != nil {
		return errors.New("could not initialize Docker API client")
	}

	if _, err := cli.ImagePull(context.Background(), image, types.ImagePullOptions{}); err != nil {
		return err
	}
	log.Debugf("docker pull: %s...Done", image)
	return nil
}

func dockerClient() (*client.Client, error) {
	// for maximum compatibility as we use nothing new
	err := os.Setenv("DOCKER_API_VERSION", "1.23")
	if err != nil {
		return nil, err
	}
	return client.NewEnvClient()
}

func dockerInspectImage(cli *client.Client, image string) (types.ImageInspect, error) {
	log.Debugf("docker inspect image: %s", image)

	inspect, _, err := cli.ImageInspectWithRaw(context.Background(), image)
	if err != nil {
		if client.IsErrImageNotFound(err) {
			pullErr := dockerPull(image, false)
			if pullErr != nil {
				return types.ImageInspect{}, pullErr
			}
			inspect, _, err = cli.ImageInspectWithRaw(context.Background(), image)
			if err != nil {
				return types.ImageInspect{}, err
			}
		} else {
			return types.ImageInspect{}, err
		}
	}

	log.Debugf("docker inspect image: %s...Done", image)

	return inspect, nil
}
