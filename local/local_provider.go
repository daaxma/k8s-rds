package local

import (
	"context"
	"fmt"
	"log"
	"time"

	e "github.com/pkg/errors"
	"github.com/sorenmat/k8s-rds/crd"
	"github.com/sorenmat/k8s-rds/provider"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Local struct {
	ServiceProvider provider.ServiceProvider
	kc              kubernetes.Interface
	SkipWaiting     bool
	repository      string
}

func New(db *crd.Database, kc kubernetes.Interface, repository string) (*Local, error) {
	r := Local{kc: kc, repository: repository}
	return &r, nil
}

// CreateDatabase creates a database from the CRD database object, is also ensures that the correct
// subnets are created for the database so we can access it
func (l *Local) CreateDatabase(ctx context.Context, db *crd.Database) (string, error) {

	if err := l.createPVC(ctx, db.Name, db.Namespace, db.Spec.Size); err != nil {
		return "", err
	}

	_new := false
	d, err := l.kc.AppsV1().Deployments(db.Namespace).Get(ctx, db.Name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		// we got an error and it's not the NotFound, let's crash
		return "", err
	}
	if errors.IsNotFound(err) {
		// Deployment seems to be empty, let's assume it means we need to create it
		d = &v1.Deployment{}
		_new = true
	}

	d.Name = db.Name
	d.Labels = map[string]string{"db": "true"}

	d.ObjectMeta = metav1.ObjectMeta{
		Name: db.Name,
	}
	d.Spec = toSpec(db, l.repository)

	if _new {
		log.Printf("creating database %v", db.Name)
		_, err = l.kc.AppsV1().Deployments(db.Namespace).Create(ctx, d, metav1.CreateOptions{})
		if err != nil {
			return "", err
		}
	} else {
		log.Printf("updating database %v", db.Name)
		_, err = l.kc.AppsV1().Deployments(db.Namespace).Update(ctx, d, metav1.UpdateOptions{})
		if err != nil {
			return "", err
		}
	}

	return db.Name, nil
}

const (
	defaultLocalRDSPVSizeUnit = "Gi"
	maxAmountOfWaitIterations = 100
	iterationWaitPeriodSec    = 5 * time.Second
)

func (l *Local) createPVC(ctx context.Context, name, namespace string, size int64) error {
	newPVC := false

	pvc, err := l.kc.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		// we got an error and it's not the NotFound, let's crash
		return err
	}
	if errors.IsNotFound(err) {
		// Deployment seems to be empty, let's assume it means we need to create it
		pvc = &corev1.PersistentVolumeClaim{}
		newPVC = true
	}

	pvc.Name = name
	pvc.ObjectMeta = metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"app": name,
		},
	}

	pvc.Annotations = map[string]string{
		"repository": "https://github.com/sorenmat/k8s-rds",
	}

	storageClass := "default"

	pvc.Spec = corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{
			"ReadWriteOnce",
		},

		Resources: corev1.ResourceRequirements{

			Requests: corev1.ResourceList{
				"storage": resource.MustParse(fmt.Sprintf("%d%s",
					size, defaultLocalRDSPVSizeUnit)),
			},
		},

		StorageClassName: &storageClass,
	}

	if newPVC {
		log.Printf("creating pvc %v", name)
		_, err = l.kc.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	} else {
		log.Printf("updating pvc %v", name)
		oldPvc, err := l.kc.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if oldPvc.Spec.Resources.Requests.StorageEphemeral().Cmp(*pvc.Spec.Resources.Requests.StorageEphemeral()) == 0 {
			log.Printf("Specs %s has same size: not updating pvc \n",
				name)
			return nil
		}
		_, err = l.kc.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{})
		if err != nil {
			return e.Wrap(err,
				fmt.Sprintf("Error: PVC %s has problems while updating %v", name, err))
		}
	}

	if !l.SkipWaiting {
		pvcIsReady := false
		for i := 0; i < maxAmountOfWaitIterations; i++ {

			pvc, err := l.kc.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})

			if err != nil {
				return e.Wrap(err, "problem of getting pvcs")
			}
			if pvc.Status.Phase == "Bound" {
				pv, err := l.kc.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
				if err != nil {
					return e.Wrap(err, "problem of getting pv")
				}
				if pv.Status.Phase == "Bound" {
					pvcIsReady = true
					break
				}
			}
			time.Sleep(iterationWaitPeriodSec)
		}

		if pvcIsReady {
			log.Printf("pvc %v is ready (bound)\n", name)
			return nil
		}

		return fmt.Errorf("Max amount of wait iterations for pvc %s being bound is expired",
			name)
	}

	return nil
}

const (
	nDeleteAttempts = 20
)

// DeleteDatabase deletes the db pod and pvc
func (l *Local) DeleteDatabase(ctx context.Context, db *crd.Database) error {
	// delete the database instance

	for i := 0; i < nDeleteAttempts; i++ {
		if err := l.kc.AppsV1().Deployments(db.Namespace).Delete(ctx, db.Name, metav1.DeleteOptions{}); err != nil {
			fmt.Printf("ERROR: error while deleting the deployment: %v\n", err)
			continue
		}

		if db.Spec.DeleteProtection {
			log.Printf("Trying to delete a %v in %v which is a deleted protected database", db.Name, db.Namespace)
		} else {
			if err := l.kc.CoreV1().PersistentVolumeClaims(db.Namespace).Delete(ctx, db.Name, metav1.DeleteOptions{}); err != nil {
				fmt.Printf("ERROR: error while deleting the pvc: %v\n", err)
				continue
			}
		}

		return nil
	}

	return fmt.Errorf("the number of attempts to delete db %s has exceeded", db.ObjectMeta.Name)
}

func int32Ptr(i int32) *int32 { return &i }

func toSpec(db *crd.Database, repository string) v1.DeploymentSpec {
	version := db.Spec.Version
	if version == "" {
		version = "latest"
	}

	image := fmt.Sprintf("%v:%v", db.Spec.Engine, version)
	if repository != "" {
		image = fmt.Sprintf("%v/%v:%v", repository, db.Spec.Engine, version)
	}
	return v1.DeploymentSpec{
		Replicas: int32Ptr(1),
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"db": db.Name,
			},
		},
		Strategy: v1.DeploymentStrategy{
			Type: "Recreate",
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"db": db.Name,
				},
			},
			Spec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{
					{
						Effect:   "NoSchedule",
						Key:      "ARM",
						Operator: "Equal",
						Value:    "TRUE",
					},
				},
				Affinity: &corev1.Affinity{
					NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Operator: "In",
											Key:      "kubernetes.io/arch",
											Values:   []string{"arm64"},
										},
									},
								},
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  db.Name,
						Image: image, // TODO is this correct
						Env: []corev1.EnvVar{corev1.EnvVar{
							Name: "POSTGRES_PASSWORD",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: db.Spec.Password.Name,
									},
									Key: db.Spec.Password.Key,
								},
							},
						},
							corev1.EnvVar{Name: "POSTGRES_USER", Value: db.Spec.Username},
							corev1.EnvVar{Name: "POSTGRES_DB", Value: db.Spec.DBName},
							corev1.EnvVar{Name: "PGDATA",
								Value: "/var/lib/postgresql/data/pgdata"},
						},
						VolumeMounts: []corev1.VolumeMount{
							corev1.VolumeMount{
								Name:      fmt.Sprintf("%s-data", db.Name),
								MountPath: "/var/lib/postgresql/data",
							},
						},
						Args: []string{
							"-N",
							"400",
						},
						Ports: []corev1.ContainerPort{
							{
								Name:          "pgsql",
								Protocol:      corev1.ProtocolTCP,
								ContainerPort: 5432,
							},
						},
					},
				},

				Volumes: []corev1.Volume{
					corev1.Volume{
						Name: fmt.Sprintf("%s-data", db.Name),
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: db.Name,
							},
						},
					},
				},
			},
		},
	}

}
