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
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
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
)

func (r PodTestRunner) createStorage(ctx context.Context, labels map[string]string) (pvcName string, err error) {
	pvcName = fmt.Sprintf("scorecard-pvc-%s", rand.String(4))
	accessModeEntered := labels[STORAGE_ACCESSMODE_LABEL]

	pvcSize := STORAGE_SIZE_DEFAULT
	if pvcSize != "" {
		pvcSize = labels[STORAGE_SIZE_LABEL]
	}
	storageClassName, err := r.findDefaultStorageClassName()
	if err != nil {
		return "", err
	}
	if labels[STORAGE_CLASS_LABEL] != "" {
		storageClassName = labels[STORAGE_CLASS_LABEL]
	}
	pvcDef, err := r.getPVCDefinition(r.configMapName, pvcName, pvcSize, storageClassName, accessModeEntered)
	if err != nil {
		return "", err
	}

	_, err = r.Client.CoreV1().PersistentVolumeClaims(r.Namespace).Create(ctx, pvcDef, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}

	return pvcName, nil
}

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

func (r PodTestRunner) findDefaultStorageClassName() (defaultStorageClassName string, err error) {
	var storageClasses *storagev1.StorageClassList
	storageClasses, err = r.Client.StorageV1().StorageClasses().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, sc := range storageClasses.Items {
		if sc.ObjectMeta.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			return sc.Name, nil
		}

	}
	return "", nil
}

func findStorageClass(scList *storagev1.StorageClassList, scName string) bool {
	for _, sc := range scList.Items {
		if sc.Name == scName {
			return true
		}

	}
	return false
}

func (r PodTestRunner) deletePVCs(ctx context.Context) error {
	selector := fmt.Sprintf("testrun=%s", r.configMapName)
	listOptions := metav1.ListOptions{LabelSelector: selector}
	err := r.Client.CoreV1().PersistentVolumeClaims(r.Namespace).DeleteCollection(ctx, metav1.DeleteOptions{}, listOptions)
	if err != nil {
		return fmt.Errorf("error deleting PVCs (label selector %q): %w", selector, err)
	}
	return nil
}

func (r PodTestRunner) getPVCDefinition(configMapName, pvcName, pvcSize, storageClassName, accessModeSupplied string) (value *v1.PersistentVolumeClaim, err error) {

	accessMode := STORAGE_DEFAULT_ACCESSMODE

	switch accessModeSupplied {
	case "":
	case "ReadWriteOnce":
		break
	case "ReadOnlyMany":
		accessMode = v1.ReadOnlyMany
		break
	case "ReadWriteMany":
		accessMode = v1.ReadWriteMany
		break
	default:
		return nil, errors.New("invalid storage accessmode, valid values are: ReadOnlyMany, ReadWriteMany, ReadWriteOnce")
	}

	qtyString := STORAGE_SIZE_DEFAULT
	q, err := resource.ParseQuantity(qtyString)
	if err != nil {
		return nil, err
	}

	resources := v1.ResourceRequirements{}
	resources.Requests = v1.ResourceList{}
	resources.Requests["storage"] = q

	value = &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"app":     "scorecard-test",
				"testrun": configMapName,
			},
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes:      []v1.PersistentVolumeAccessMode{accessMode},
			Resources:        resources,
			StorageClassName: &storageClassName,
		},
	}
	return value, nil
}

func addStorageToPod(podDef *v1.Pod, pvcName string) {
	vMount := v1.VolumeMount{
		MountPath: STORAGE_DEFAULT_MOUNT,
		Name:      "scorecardstorage",
		ReadOnly:  false,
	}
	podDef.Spec.Containers[0].VolumeMounts = append(podDef.Spec.Containers[0].VolumeMounts, vMount)

	newVolume := v1.Volume{}
	newVolume.Name = "scorecardstorage"
	newVolume.VolumeSource = v1.VolumeSource{}
	newVolume.PersistentVolumeClaim = &v1.PersistentVolumeClaimVolumeSource{}
	newVolume.PersistentVolumeClaim.ClaimName = pvcName
	newVolume.PersistentVolumeClaim.ReadOnly = false

	podDef.Spec.Volumes = append(podDef.Spec.Volumes, newVolume)

}

func getGatherPodDefinition(podName, pvcName string, r PodTestRunner) *v1.Pod {

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"app":     "scorecard-gather",
				"testrun": r.configMapName,
			},
		},
		Spec: v1.PodSpec{
			ServiceAccountName: r.ServiceAccount,
			RestartPolicy:      v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:            "scorecard-gather",
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

func gatherTestOutput(ctx context.Context, r PodTestRunner, suiteName, testName, pvcName string) error {
	// use the pvcName, being unique, as the pod name which needs to be unique
	podName := fmt.Sprintf("scorecard-gather-%s", rand.String(4))
	podDef := getGatherPodDefinition(podName, pvcName, r)
	pod, err := r.Client.CoreV1().Pods(r.Namespace).Create(ctx, podDef, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	//fmt.Printf("gathering test output pod %s in ns %s outputdir %s testName %s pvcName %s\n", pod.ObjectMeta.Name, r.Namespace, r.TestOutput, testName, pvcName)

	// wait for the gather pod to be in Running state so we can exec into it
	err = r.waitForTestToRun(ctx, pod)
	if err != nil {
		return err
	}

	//exec into pod, run tar,  get reader
	mountPath := STORAGE_DEFAULT_MOUNT
	containerName := "scorecard-gather"
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

// waitForTestToRun waits for a fixed amount of time while
// checking for a test pod to be in a Running state
func (r PodTestRunner) waitForTestToRun(ctx context.Context, p *v1.Pod) (err error) {

	podCheck := wait.ConditionFunc(func() (done bool, err error) {
		var tmp *v1.Pod
		tmp, err = r.Client.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		if err != nil {
			return true, fmt.Errorf("error getting pod %s %w", p.Name, err)
		}
		if tmp.Status.Phase == v1.PodRunning {
			return true, nil
		}
		return false, nil
	})

	err = wait.PollImmediateUntil(1*time.Second, podCheck, ctx.Done())
	return err

}

func getDestPath(baseDir, suiteName, testName string) (destPath string) {
	destPath = baseDir + string(os.PathSeparator)
	if suiteName != "" {
		destPath = destPath + suiteName + string(os.PathSeparator)
	}
	destPath = destPath + testName
	return destPath
}
