//
//  This file is part of arduino-connector
//
//  Copyright (C) 2017-2018  Arduino AG (http://www.arduino.cc/)
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	apt "github.com/arduino/go-apt-client"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	docker "github.com/docker/docker/client"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pkg/errors"
	"github.com/shirou/gopsutil/host"
	"golang.org/x/net/context"
)

// checkAndInstallDocker implements steps from https://docs.docker.com/install/linux/docker-ce/ubuntu/
func checkAndInstallDocker() {
	cli, err := docker.NewEnvClient()
	defer cli.Close()
	if cli != nil {
		_, err = cli.ContainerList(context.Background(), types.ContainerListOptions{})
		if err != nil {
			fmt.Println("Docker daemon not found!")
		}
	}
	if err != nil {
		//returns  platform string, family string, version string, err error
		platform, family, version, err := host.PlatformInformation()
		distroVer, cerr := strconv.Atoi(strings.Replace(version, ".", "", -1))
		if err != nil && cerr != nil {
			fmt.Println("Failed to fetch system info")
		}
		fmt.Printf("Fetched system info: %s %s %s on arch: %s\n", platform, family, version, runtime.GOARCH)
		if runtime.GOARCH == "amd64" {
			if platform == "ubuntu" {
				if distroVer >= 1604 {
					installDockerCEOnXenialAndNewer()
				}
			}
		} else if runtime.GOARCH == "arm" {
			if platform == "raspbian" {
				installDockerCEViaConvenienceScript()
			}
		}
	}
}

func installDockerCEViaConvenienceScript() {
	curlString := "curl -fsSL get.docker.com -o get-docker.sh"
	curlCmd := exec.Command("bash", "-c", curlString)
	if out, err := curlCmd.CombinedOutput(); err != nil {
		fmt.Println("Failed to Download Docker CE Convenience Script Installer:")
		fmt.Println(string(out))
	}

	installCmd := exec.Command("sh", "get-docker.sh")
	if out, err := installCmd.CombinedOutput(); err != nil {
		fmt.Println("Failed to Run Docker CE Convenience Script Installer:")
		fmt.Println(string(out))
	}
}

func installDockerCEOnXenialAndNewer() {
	// dpkg --configure -a for prevent block of installation
	dpkgCmd := exec.Command("dpkg", "--configure", "-a")
	if out, err := dpkgCmd.CombinedOutput(); err != nil {
		fmt.Println("Failed to reconfigure dpkg:")
		fmt.Println(string(out))
	}

	apt.CheckForUpdates()
	dockerPrerequisitesPackages := []*apt.Package{
		&apt.Package{Name: "apt-transport-https"},
		&apt.Package{Name: "ca-certificates"},
		&apt.Package{Name: "curl"},
		&apt.Package{Name: "software-properties-common"},
	}
	for _, pac := range dockerPrerequisitesPackages {
		if out, err := apt.Install(pac); err != nil {
			fmt.Println("Failed to install: ", pac.Name)
			fmt.Println(string(out))
			return
		}
	}

	curlString := "curl -fsSL https://download.docker.com/linux/ubuntu/gpg | apt-key add -"
	curlCmd := exec.Command("bash", "-c", curlString)
	if out, err := curlCmd.CombinedOutput(); err != nil {
		fmt.Println("Failed to add Docker’s official GPG key:")
		fmt.Println(string(out))
	}

	repoString := `add-apt-repository "deb [arch=amd64] https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable"`
	repoCmd := exec.Command("bash", "-c", repoString)
	if out, err := repoCmd.CombinedOutput(); err != nil {
		fmt.Println("Failed to set up the stable docker repository:")
		fmt.Println(string(out))
	}

	apt.CheckForUpdates()
	toInstall := &apt.Package{Name: "docker-ce"}
	if out, err := apt.Install(toInstall); err != nil {
		fmt.Println("Failed to install docker-ce:")
		fmt.Println(string(out))
		return
	}

	sysCmd := exec.Command("systemctl", "enable", "docker")
	if out, err := sysCmd.CombinedOutput(); err != nil {
		fmt.Println("Failed to systemctl enable docker:")
		fmt.Println(string(out))
	}
}

// ContainersPsEvent implements docker ps -a
func (s *Status) ContainersPsEvent(client mqtt.Client, msg mqtt.Message) {

	containers, err := s.dockerClient.ContainerList(context.Background(), types.ContainerListOptions{All: true})
	if err != nil {
		s.Error("/containers/ps", fmt.Errorf("Json marshal result: %s", err))
		return
	}

	// Send result
	data, err := json.Marshal(containers)
	if err != nil {
		s.Error("/containers/ps", fmt.Errorf("Json marsahl result: %s", err))
		return
	}
	s.Info("/containers/ps", string(data)+"\n")
}

// ContainersListImagesEvent implements docker images
func (s *Status) ContainersListImagesEvent(client mqtt.Client, msg mqtt.Message) {
	images, err := s.dockerClient.ImageList(context.Background(), types.ImageListOptions{})
	if err != nil {
		s.Error("/containers/images", fmt.Errorf("images result: %s", err))
		return
	}

	// Send result
	data, err := json.Marshal(images)
	if err != nil {
		s.Error("/containers/images", fmt.Errorf("Json marsahl result: %s", err))
		return
	}

	s.Info("/containers/images", string(data)+"\n")
}

// ContainersActionEvent implements docker container action like run, start and stop, remove
func (s *Status) ContainersActionEvent(client mqtt.Client, msg mqtt.Message) {

	type RunPayload struct {
		ImageName               string                   `json:"image"`
		ContainerName           string                   `json:"name"`
		RunAsDaemon             bool                     `json:"background"`
		ContainerID             string                   `json:"id"`
		Action                  string                   `json:"action"`
		ContainerConfig         container.Config         `json:"container_config",omitempty`
		ContainerHostConfig     container.HostConfig     `json:"host_config",omitempty`
		NetworkNetworkingConfig network.NetworkingConfig `json:"networking_config",omitempty`
	}

	runParams := RunPayload{}
	err := json.Unmarshal(msg.Payload(), &runParams)
	if err != nil {
		s.Error("/containers/action", errors.Wrapf(err, "unmarshal %s", msg.Payload()))
		return
	}

	runResponse := RunPayload{
		ImageName:     runParams.ImageName,
		ContainerName: runParams.ContainerName,
		RunAsDaemon:   runParams.RunAsDaemon,
		ContainerID:   runParams.ContainerID,
		Action:        runParams.Action,
	}

	ctx := context.Background()
	switch runParams.Action {
	case "run":
		out, err := s.dockerClient.ImagePull(ctx, runParams.ImageName, types.ImagePullOptions{})
		if err != nil {
			s.Error("/containers/action", fmt.Errorf("image pull result: %s", err))
			return
		}
		// waiting the complete download of the image
		io.Copy(ioutil.Discard, out)
		defer out.Close()
		fmt.Fprintf(os.Stdout, "Successfully Downloaded Image: %s\n", runParams.ImageName)

		// overwrite imagename in container.Config
		runParams.ContainerConfig.Image = runParams.ImageName

		resp, err := s.dockerClient.ContainerCreate(ctx, &runParams.ContainerConfig, &runParams.ContainerHostConfig, &runParams.NetworkNetworkingConfig, runParams.ContainerName)

		if err != nil {
			s.Error("/containers/action", fmt.Errorf("container create result: %s", err))
			return
		}

		if err := s.dockerClient.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
			s.Error("/containers/action", fmt.Errorf("container start result: %s", err))
			return
		}
		runResponse.ContainerID = resp.ID

	case "stop":
		if err := s.dockerClient.ContainerStop(ctx, runParams.ContainerID, nil); err != nil {
			s.Error("/containers/action", fmt.Errorf("container action result: %s", err))
			return
		}

	case "start":
		if err := s.dockerClient.ContainerStart(ctx, runParams.ContainerID, types.ContainerStartOptions{}); err != nil {
			s.Error("/containers/action", fmt.Errorf("container action result: %s", err))
			return
		}

	case "remove":
		forceAllOption := types.ContainerRemoveOptions{
			Force:         true,
			RemoveLinks:   false,
			RemoveVolumes: true,
		}

		if err := s.dockerClient.ContainerRemove(ctx, runParams.ContainerID, forceAllOption); err != nil {
			s.Error("/containers/action", fmt.Errorf("container remove result: %s", err))
			return
		}
		// implements docker image prune -a that removes all images not associated to a container
		forceAllImagesArg, _ := filters.FromJSON(`{"dangling": false}`)
		if _, err := s.dockerClient.ImagesPrune(ctx, forceAllImagesArg); err != nil {
			s.Error("/containers/action", fmt.Errorf("images prune result: %s", err))
			return
		}

	default:
		s.Error("/containers/action", fmt.Errorf("container command %s not found", runParams.Action))
		return
	}

	// Send result
	data, err := json.Marshal(runResponse)
	if err != nil {
		s.Error("/containers/action", fmt.Errorf("Json marshal result: %s", err))
		return
	}

	s.Info("/containers/action", string(data)+"\n")

}
