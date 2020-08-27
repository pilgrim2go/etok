package cmd

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/apex/log"
	"github.com/leg100/stok/api/v1alpha1"
	v1alpha1types "github.com/leg100/stok/api/v1alpha1"
	"github.com/leg100/stok/pkg/k8s"
	"github.com/leg100/stok/version"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type newWorkspaceCmd struct {
	Name          string
	Namespace     string
	Path          string
	Timeout       time.Duration
	TimeoutPod    time.Duration
	Context       string
	WorkspaceSpec v1alpha1.WorkspaceSpec

	factory k8s.FactoryInterface
	debug   bool
	out     io.Writer
	cmd     *cobra.Command
}

func newNewWorkspaceCmd(f k8s.FactoryInterface, out io.Writer) *cobra.Command {
	cc := &newWorkspaceCmd{}
	cc.cmd = &cobra.Command{
		Use:   "new <workspace>",
		Short: "Create a new stok workspace",
		Long:  "Deploys a Workspace resource",
		Args:  cobra.ExactArgs(1),
		RunE:  cc.doNewWorkspace,
	}
	cc.cmd.Flags().StringVar(&cc.Path, "path", ".", "workspace config path")
	cc.cmd.Flags().StringVar(&cc.Namespace, "namespace", "default", "Kubernetes namespace of workspace")

	cc.cmd.Flags().StringVar(&cc.WorkspaceSpec.ServiceAccountName, "service-account", "", "Name of ServiceAccount")
	cc.cmd.Flags().StringVar(&cc.WorkspaceSpec.SecretName, "secret", "", "Name of Secret containing credentials")

	cc.cmd.Flags().StringVar(&cc.WorkspaceSpec.Cache.Size, "size", "1Gi", "Size of PersistentVolume for cache")
	cc.cmd.Flags().StringVar(&cc.WorkspaceSpec.Cache.StorageClass, "storage-class", "", "StorageClass of PersistentVolume for cache")
	cc.cmd.Flags().DurationVar(&cc.Timeout, "timeout", 10*time.Second, "Time to wait for workspace to be healthy")

	// Validate
	cc.cmd.Flags().StringVar(&cc.WorkspaceSpec.TimeoutClient, "timeout-client", "10s", "timeout for client to signal readiness")

	cc.cmd.Flags().DurationVar(&cc.TimeoutPod, "timeout-pod", time.Minute, "timeout for pod to be ready and running")
	cc.cmd.Flags().StringVar(&cc.Context, "context", "", "Set kube context (defaults to kubeconfig current context)")

	cc.cmd.Flags().StringVar(&cc.WorkspaceSpec.Backend.Type, "backend-type", "local", "Set backend type")
	cc.cmd.Flags().StringToStringVar(&cc.WorkspaceSpec.Backend.Config, "backend-config", map[string]string{}, "Set backend config (command separated key values, e.g. bucket=gcs,prefix=dev")

	// Add flags registered by imported packages (controller-runtime)
	cc.cmd.Flags().AddGoFlagSet(flag.CommandLine)

	cc.factory = f
	cc.out = out

	return cc.cmd
}

func CheckResourceExists(ctx context.Context, rc client.Client, name, namespace string, obj runtime.Object) (bool, error) {
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	if err := rc.Get(ctx, nn, obj); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, err
		}
	}
	return true, nil
}

// Create new workspace. First check values of secret and service account flags, if either are empty
// then search for respective resources named "stok" and if they exist, set in the workspace spec
// accordingly. Otherwise use user-supplied values and check they exist. Only then create the
// workspace resource and wait until it is reporting it is healthy, or the timeout expires.
func (t *newWorkspaceCmd) doNewWorkspace(cmd *cobra.Command, args []string) error {
	debug, err := cmd.InheritedFlags().GetBool("debug")
	if err != nil {
		return err
	}
	t.debug = debug

	t.Name = args[0]

	ctx := cmd.Context()

	config, err := t.factory.NewConfig(t.Context)
	if err != nil {
		return err
	}

	rc, err := t.factory.NewClient(config)
	if err != nil {
		return err
	}

	// Delete any created resources upon program termination.
	var resources []runtime.Object
	go func() {
		<-ctx.Done()

		for _, r := range resources {
			rc.Delete(context.TODO(), r)
		}
	}()

	if t.WorkspaceSpec.SecretName != "" {
		// Secret specified; check that it exists and if not found then error
		found, err := CheckResourceExists(ctx, rc, t.WorkspaceSpec.SecretName, t.Namespace, &corev1.Secret{})
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("secret '%s' not found", t.WorkspaceSpec.SecretName)
		}
	} else {
		// Secret unspecified, check if resource called 'stok' exists and if so, use that
		found, err := CheckResourceExists(ctx, rc, "stok", t.Namespace, &corev1.Secret{})
		if err != nil {
			return err
		}
		if found {
			t.WorkspaceSpec.SecretName = "stok"
			log.Info("Found default secret...")
		}
	}

	if t.WorkspaceSpec.ServiceAccountName != "" {
		// Service account specified; check that it exists and if not found then error
		found, err := CheckResourceExists(ctx, rc, t.WorkspaceSpec.ServiceAccountName, t.Namespace, &corev1.ServiceAccount{})
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("service account '%s' not found", t.WorkspaceSpec.ServiceAccountName)
		}
	} else {
		// Service account unspecified, check if resource called 'stok' exists and if so, use that
		found, err := CheckResourceExists(ctx, rc, "stok", t.Namespace, &corev1.ServiceAccount{})
		if err != nil {
			return err
		}
		if found {
			t.WorkspaceSpec.ServiceAccountName = "stok"
			log.Info("Found default service account...")
		}
	}

	ws, err := t.createWorkspace(rc)
	if err != nil {
		return err
	}
	resources = append(resources, ws)
	log.WithFields(log.Fields{"namespace": t.Namespace, "workspace": t.Name}).Info("created workspace")

	// Instantiate k8s cache mgr
	mgr, err := t.factory.NewManager(config, t.Namespace)
	if err != nil {
		return err
	}

	// Run workspace reporter, which waits until the status reports the pod is ready
	mgr.AddReporter(&WorkspaceReporter{
		Client: rc,
		Id:     t.Name,
	})

	// Run workspace reporter, which waits until the status reports the pod is ready
	mgr.AddReporter(&WorkspacePodReporter{
		Client: rc,
		Id:     ws.PodName(),
	})

	// Run cache mgr, blocking until the reporter returns successfully, indicating that we can
	// proceed to connecting to the pod.
	if err := mgr.Start(ctx); err != nil {
		return err
	}

	// Get pod
	pod := &corev1.Pod{}
	if err := rc.Get(ctx, types.NamespacedName{Name: ws.PodName(), Namespace: t.Namespace}, pod); err != nil {
		return err
	}

	podlog := log.WithField("pod", k8s.GetNamespacedName(pod))

	podlog.Debug("retrieve log stream")
	logstream, err := rc.GetLogs(t.Namespace, ws.PodName(), &corev1.PodLogOptions{Follow: true, Container: "runner"})
	if err != nil {
		return err
	}
	defer logstream.Close()

	// Attach to pod tty
	done := make(chan error)
	go func() {
		podlog.Debug("attaching")
		done <- k8s.AttachFallbackToLogs(rc, pod, logstream)
	}()

	// Let operator know we're now streaming logs
	if err := k8s.ReleaseHold(ctx, rc, ws); err != nil {
		return err
	}

	if err := <-done; err != nil {
		return err
	}

	if err := writeEnvironmentFile(t.Path, t.Namespace, t.Name); err != nil {
		return err
	}

	return nil
}

var WorkspaceTimeoutErr = fmt.Errorf("timed out waiting for workspace to be in a healthy condition")

func (t *newWorkspaceCmd) createWorkspace(rc client.Client) (*v1alpha1types.Workspace, error) {
	ws := &v1alpha1types.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      t.Name,
			Namespace: t.Namespace,
			Labels: map[string]string{
				// Name of the application
				"app":                    "stok",
				"app.kubernetes.io/name": "stok",

				// Name of higher-level application this app is part of
				"app.kubernetes.io/part-of": "stok",

				// The tool being used to manage the operation of an application
				"app.kubernetes.io/managed-by": "stok-operator",

				// Unique name of instance within application
				"app.kubernetes.io/instance": t.Name,

				// Current version of application
				"version":                   version.Version,
				"app.kubernetes.io/version": version.Version,

				// Component within architecture
				"component":                   "workspace",
				"app.kubernetes.io/component": "workspace",
			},
		},
		Spec: t.WorkspaceSpec,
	}

	ws.SetAnnotations(map[string]string{v1alpha1.WaitAnnotationKey: "true"})
	ws.SetDebug(t.debug)

	err := rc.Create(context.TODO(), ws)
	return ws, err
}
