// Copyright 2021 The Operator-SDK Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scorecard

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

const (
	STORAGE_PROVISION_LABEL    = "storage"
	STORAGE_SIZE_LABEL         = "storage-size"
	STORAGE_ACCESSMODE_LABEL   = "storage-accessmode"
	STORAGE_SIZE_DEFAULT       = "1Gi"
	STORAGE_MOUNT_LABEL        = "storage-mount"
	STORAGE_CLASS_LABEL        = "storage-class"
	STORAGE_DEFAULT_MOUNT      = "/test-output"
	STORAGE_DEFAULT_ACCESSMODE = v1.ReadWriteOnce
	STORAGE_SIDECAR_CONTAINER  = "scorecard-gather"
)

func (r PodTestRunner) execInPod(podName, mountPath, containerName string) (io.Reader, error) {
	cmd := []string{
		"tar",
		"cf",
		"-",
		mountPath,
	}

	reader, outStream := io.Pipe()
	const tty = false
	req := r.Client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(r.Namespace).SubResource("exec").Param("container", containerName)
	req.VersionedParams(
		&v1.PodExecOptions{
			Command: cmd,
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     tty,
		},
		scheme.ParameterCodec,
	)

	var stderr bytes.Buffer
	exec, err := remotecommand.NewSPDYExecutor(r.RESTConfig, "POST", req.URL())
	if err != nil {
		return nil, err
	}

	go func() {
		defer outStream.Close()
		err = exec.Stream(remotecommand.StreamOptions{
			Stdin:  nil,
			Stdout: outStream,
			Stderr: &stderr,
		})
		if err != nil {
			log.Error(err)
		}
	}()
	return reader, err
}

func getStoragePrefix(file string) string {
	return strings.TrimLeft(file, "/")
}

func untarAll(reader io.Reader, destDir, prefix string) error {
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err != nil {
			if err != io.EOF {
				return err
			}
			break
		}

		if !strings.HasPrefix(header.Name, prefix) {
			return fmt.Errorf("tar contents corrupted")
		}

		mode := header.FileInfo().Mode()
		destFileName := filepath.Join(destDir, header.Name[len(prefix):])

		baseName := filepath.Dir(destFileName)
		if err := os.MkdirAll(baseName, 0755); err != nil {
			return err
		}
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(destFileName, 0755); err != nil {
				return err
			}
			continue
		}
		evaledPath, err := filepath.EvalSymlinks(baseName)
		if err != nil {
			return err
		}

		if mode&os.ModeSymlink != 0 {
			linkname := header.Linkname

			if !filepath.IsAbs(linkname) {
				_ = filepath.Join(evaledPath, linkname)
			}

			if err := os.Symlink(linkname, destFileName); err != nil {
				return err
			}
		} else {
			outFile, err := os.Create(destFileName)
			if err != nil {
				return err
			}
			defer outFile.Close()
			if _, err := io.Copy(outFile, tarReader); err != nil {
				return err
			}
			if err := outFile.Close(); err != nil {
				return err
			}
		}
	}

	return nil
}

func addStorageToPod(podDef *v1.Pod) {

	// add the emptyDir volume for storage to the test Pod
	newVolume := v1.Volume{}
	newVolume.Name = "scorecard-storage"
	newVolume.VolumeSource = v1.VolumeSource{}

	podDef.Spec.Volumes = append(podDef.Spec.Volumes, newVolume)

	// add the storage sidecar container
	storageContainer := v1.Container{
		Name:            STORAGE_SIDECAR_CONTAINER,
		Image:           "busybox",
		ImagePullPolicy: v1.PullIfNotPresent,
		Args: []string{
			"sleep",
			"300",
		},
		VolumeMounts: []v1.VolumeMount{
			{
				MountPath: STORAGE_DEFAULT_MOUNT,
				Name:      "scorecard-storage",
				ReadOnly:  true,
			},
		},
	}

	podDef.Spec.Containers = append(podDef.Spec.Containers, storageContainer)

	// add the storage emptyDir volume into the test container

	vMount := v1.VolumeMount{
		MountPath: STORAGE_DEFAULT_MOUNT,
		Name:      "scorecard-storage",
		ReadOnly:  false,
	}
	podDef.Spec.Containers[0].VolumeMounts = append(podDef.Spec.Containers[0].VolumeMounts, vMount)

}

func getGatherPodDefinition(podName, pvcName string, r PodTestRunner) *v1.Pod {

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"app":     STORAGE_SIDECAR_CONTAINER,
				"testrun": r.configMapName,
			},
		},
		Spec: v1.PodSpec{
			ServiceAccountName: r.ServiceAccount,
			RestartPolicy:      v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:            STORAGE_SIDECAR_CONTAINER,
					Image:           "busybox",
					ImagePullPolicy: v1.PullIfNotPresent,
					Args: []string{
						"sleep",
						"300",
					},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: STORAGE_DEFAULT_MOUNT,
							Name:      "test-output",
							ReadOnly:  true,
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "test-output",
					VolumeSource: v1.VolumeSource{
						PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
							ReadOnly:  true,
						},
					},
				},
			},
		},
	}
}

func gatherTestOutput(ctx context.Context, r PodTestRunner, suiteName, testName, podName string) error {

	//exec into sidecar container, run tar,  get reader
	mountPath := STORAGE_DEFAULT_MOUNT
	containerName := STORAGE_SIDECAR_CONTAINER
	reader, err := r.execInPod(podName, mountPath, containerName)
	if err != nil {
		return err
	}

	srcPath := r.TestOutput
	prefix := getStoragePrefix(srcPath)
	prefix = path.Clean(prefix)
	destPath := getDestPath(r.TestOutput, suiteName, testName)
	fmt.Printf("destPath becomes %s\n", destPath)
	err = untarAll(reader, destPath, prefix)
	if err != nil {
		return err
	}

	return nil
}

func getDestPath(baseDir, suiteName, testName string) (destPath string) {
	destPath = baseDir + string(os.PathSeparator)
	if suiteName != "" {
		destPath = destPath + suiteName + string(os.PathSeparator)
	}
	destPath = destPath + testName
	return destPath
}
