package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"strings"
)

type tarWriter interface {
	Close() error
	Flush() error
	Write(b []byte) (n int, err error)
	WriteHeader(hdr *tar.Header) error
}

// This uses Docker to convert a Docker image into a tarball. It would be an improvement if we
// used the containerd libraries to do this instead locally direct from a local image
// cache as it would be much simpler.

var exclude = map[string]bool{
	".dockerenv":   true,
	"Dockerfile":   true,
	"dev/console":  true,
	"dev/pts":      true,
	"dev/shm":      true,
	"etc/hostname": true,
}

var replace = map[string]string{
	"etc/hosts": `127.0.0.1       localhost
::1     localhost ip6-localhost ip6-loopback
fe00::0 ip6-localnet
ff00::0 ip6-mcastprefix
ff02::1 ip6-allnodes
ff02::2 ip6-allrouters
`,
	"etc/resolv.conf": `nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 2001:4860:4860::8888
nameserver 2001:4860:4860::8844
`,
}

// tarPrefix creates the leading directories for a path
func tarPrefix(path string, tw tarWriter) error {
	if path == "" {
		return nil
	}
	if path[len(path)-1] != byte('/') {
		return fmt.Errorf("path does not end with /: %s", path)
	}
	path = path[:len(path)-1]
	if path[0] == byte('/') {
		return fmt.Errorf("path should be relative: %s", path)
	}
	mkdir := ""
	for _, dir := range strings.Split(path, "/") {
		mkdir = mkdir + dir
		hdr := &tar.Header{
			Name:     mkdir,
			Mode:     0755,
			Typeflag: tar.TypeDir,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		mkdir = mkdir + "/"
	}
	return nil
}

// ImageTar takes a Docker image and outputs it to a tar stream
func ImageTar(log Logger, image, prefix string, tw tarWriter, trust bool, pull bool) error {
	log.Debugf("image tar: %s %s", image, prefix)
	if prefix != "" && prefix[len(prefix)-1] != byte('/') {
		return fmt.Errorf("prefix does not end with /: %s", prefix)
	}

	err := tarPrefix(prefix, tw)
	if err != nil {
		return err
	}

	if pull || trust {
		err := dockerPull(log, image, pull, trust)
		if err != nil {
			return fmt.Errorf("Could not pull image %s: %v", image, err)
		}
	}
	container, err := dockerCreate(log, image)
	if err != nil {
		// if the image wasn't found, pull it down.  Bail on other errors.
		if strings.Contains(err.Error(), "No such image") {
			err := dockerPull(log, image, true, trust)
			if err != nil {
				return fmt.Errorf("Could not pull image %s: %v", image, err)
			}
			container, err = dockerCreate(log, image)
			if err != nil {
				return fmt.Errorf("Failed to docker create image %s: %v", image, err)
			}
		} else {
			return fmt.Errorf("Failed to create docker image %s: %v", image, err)
		}
	}
	contents, err := dockerExport(log, container)
	if err != nil {
		return fmt.Errorf("Failed to docker export container from container %s: %v", container, err)
	}
	err = dockerRm(log, container)
	if err != nil {
		return fmt.Errorf("Failed to docker rm container %s: %v", container, err)
	}

	// now we need to filter out some files from the resulting tar archive

	r := bytes.NewReader(contents)
	tr := tar.NewReader(r)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if exclude[hdr.Name] {
			log.Debugf("image tar: %s %s exclude %s", image, prefix, hdr.Name)
			_, err = io.Copy(ioutil.Discard, tr)
			if err != nil {
				return err
			}
		} else if replace[hdr.Name] != "" {
			contents := replace[hdr.Name]
			hdr.Size = int64(len(contents))
			hdr.Name = prefix + hdr.Name
			log.Debugf("image tar: %s %s add %s", image, prefix, hdr.Name)
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			buf := bytes.NewBufferString(contents)
			_, err = io.Copy(tw, buf)
			if err != nil {
				return err
			}
			_, err = io.Copy(ioutil.Discard, tr)
			if err != nil {
				return err
			}
		} else {
			log.Debugf("image tar: %s %s add %s", image, prefix, hdr.Name)
			hdr.Name = prefix + hdr.Name
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			_, err = io.Copy(tw, tr)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ImageBundle produces an OCI bundle at the given path in a tarball, given an image and a config.json
func ImageBundle(log Logger, path string, image string, config []byte, tw tarWriter, trust bool, pull bool) error {
	log.Debugf("image bundle: %s %s cfg: %s", path, image, string(config))
	err := ImageTar(log, image, path+"/rootfs/", tw, trust, pull)
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name: path + "/" + "config.json",
		Mode: 0644,
		Size: int64(len(config)),
	}
	err = tw.WriteHeader(hdr)
	if err != nil {
		return err
	}
	buf := bytes.NewBuffer(config)
	_, err = io.Copy(tw, buf)
	if err != nil {
		return err
	}

	return nil
}
