---
title: "operator-sdk run bundle"
---
## operator-sdk run bundle

Deploy an Operator in the bundle format with OLM

### Synopsis

The single argument to this command is a bundle image, with the full registry path specified.
If using a docker.io image, you must specify docker.io(/&lt;namespace&gt;)?/&lt;bundle-image-name&gt;:&lt;tag&gt;.

```
operator-sdk run bundle <bundle-image> [flags]
```

### Options

```
  -h, --help                            help for bundle
      --index-image string              index image in which to inject bundle (default "quay.io/operator-framework/upstream-opm-builder:latest")
      --install-mode InstallModeValue   install mode
      --kubeconfig string               Path to the kubeconfig file to use for CLI requests.
  -n, --namespace string                If present, namespace scope for this CLI request
      --secret-name string              Name of image pull secret ("type: kubernetes.io/dockerconfigjson") required to pull bundle images. This secret *must* be both in the namespace and an imagePullSecret of the service account that this command is configured to run in
      --service-account string          Service account name to bind registry objects to. If unset, the default service account is used. This value does not override the operator's service account
      --timeout duration                Duration to wait for the command to complete before failing (default 2m0s)
```

### Options inherited from parent commands

```
      --plugins strings   plugin keys to be used for this subcommand execution
      --verbose           Enable verbose logging
```

### SEE ALSO

* [operator-sdk run](../operator-sdk_run)	 - Run an Operator in a variety of environments

