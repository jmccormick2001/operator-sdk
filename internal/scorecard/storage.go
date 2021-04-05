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
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

const (
	STORAGE_PROVISION_LABEL = "storage-provision"
	STORAGE_SIZE_LABEL      = "storage-size"
	STORAGE_SIZE_DEFAULT    = "1Gi"
	STORAGE_MOUNT_LABEL     = "storage-mount"
	STORAGE_CLASS_LABEL     = "storage-class"
)

func (r PodTestRunner) createStorage(ctx context.Context, pvcName string, labels map[string]string) (err error) {
	fmt.Printf("STORAGE..creating PVC [%s]\n", pvcName)
	fmt.Printf("storage-provision [%s]\n", labels[STORAGE_PROVISION_LABEL])
	fmt.Printf("storage-size [%s]\n", labels[STORAGE_SIZE_LABEL])
	fmt.Printf("storage-mount [%s]\n", labels[STORAGE_MOUNT_LABEL])
	fmt.Printf("storage-class [%s]\n", labels[STORAGE_CLASS_LABEL])
	pvcSize := STORAGE_SIZE_DEFAULT
	if pvcSize != "" {
		pvcSize = labels[STORAGE_SIZE_LABEL]
	}
	storageClassName, err := r.findDefaultStorageClassName()
	if err != nil {
		return err
	}
	if labels[STORAGE_CLASS_LABEL] != "" {
		storageClassName = labels[STORAGE_CLASS_LABEL]
	}
	pvcDef, err := r.getPVCDefinition(pvcName, pvcSize, storageClassName)
	if err != nil {
		return err
	}
	fmt.Printf("would have created this pvc %+v\n", pvcDef)

	pvc, err := r.Client.CoreV1().PersistentVolumeClaims(r.Namespace).Create(ctx, pvcDef, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("created pvc %s!\n", pvc.Name)

	return nil
}

func execInPod(config *rest.Config, namespace, podName, command, containerName string) (io.Reader, error) {
	k8sCli, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	/**
	cmd := []string{
		"sh",
		"-c",
		command,
	}
	*/
	cmd := []string{
		"tar",
		"cf",
		"-",
		command,
	}

	reader, outStream := io.Pipe()
	const tty = false
	req := k8sCli.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).SubResource("exec").Param("container", containerName)
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

	fmt.Printf("URL %s\n", req.URL().String())
	var stderr bytes.Buffer
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return nil, err
	}

	go func() {
		defer outStream.Close()
		err = exec.Stream(remotecommand.StreamOptions{
			Stdin: nil,
			//Stdout: &stdout,
			Stdout: outStream,
			Stderr: &stderr,
		})
		if err != nil {
			fmt.Printf("error in stream %s\n", err.Error())
		}
	}()
	return reader, err
}

func getPrefix(file string) string {
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
		fmt.Printf("storageClasses %+s\n", sc.Name)
	}

	for _, sc := range storageClasses.Items {
		fmt.Printf("storageClasses %+s meta annotations %+v\n", sc.Name, sc.ObjectMeta.Annotations)
		if sc.ObjectMeta.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			fmt.Printf("this one %s is the default storage class\n", sc.Name)
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

func (r PodTestRunner) deletePVC(ctx context.Context, configMapName string) error {
	fmt.Printf("jeff cleanup pvc on %s\n", configMapName)
	err := r.Client.CoreV1().PersistentVolumeClaims(r.Namespace).Delete(ctx, configMapName, metav1.DeleteOptions{})
	if err != nil && kerrors.IsNotFound(err) {
		fmt.Printf("jeff cleanup pvc but its not found\n")
		return nil
	}
	if err != nil {
		return fmt.Errorf("error deleting PVC %s %w", configMapName, err)
	}
	fmt.Printf("jeff cleanup pvc on %s\n successful", configMapName)
	return nil
}

func (r PodTestRunner) getPVCDefinition(pvcName, pvcSize, storageClassName string) (value *v1.PersistentVolumeClaim, err error) {
	qtyString := "100Mi"
	q, err := resource.ParseQuantity(qtyString)
	if err != nil {
		return nil, err
	}

	resources := v1.ResourceRequirements{}
	resources.Requests = v1.ResourceList{}
	resources.Requests["storage"] = q
	//storage: 100Mi

	value = &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: r.Namespace,
			Labels: map[string]string{
				"app":     "scorecard-test",
				"testrun": pvcName,
			},
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes:      []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
			Resources:        resources,
			StorageClassName: &storageClassName,
		},
	}
	return value, nil
}

func addStorageToPod(podDef *v1.Pod, pvcName string) {
	vMount := v1.VolumeMount{
		MountPath: "/somestorage",
		Name:      "somestorage",
		ReadOnly:  false,
	}
	podDef.Spec.Containers[0].VolumeMounts = append(podDef.Spec.Containers[0].VolumeMounts, vMount)

	newVolume := v1.Volume{}
	newVolume.Name = "somestorage"
	newVolume.VolumeSource = v1.VolumeSource{}
	newVolume.PersistentVolumeClaim = &v1.PersistentVolumeClaimVolumeSource{}
	newVolume.PersistentVolumeClaim.ClaimName = pvcName
	newVolume.PersistentVolumeClaim.ReadOnly = false

	podDef.Spec.Volumes = append(podDef.Spec.Volumes, newVolume)

}
