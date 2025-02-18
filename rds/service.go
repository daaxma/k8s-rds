package rds

import (
	"context"
	"fmt"
	"log"

	"github.com/pkg/errors"
	"github.com/sorenmat/k8s-rds/kube"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// create an External named service object for Kubernetes
func (r *RDS) createServiceObj(s *v1.Service, namespace string, hostname string, internalname string) *v1.Service {
	var ports []v1.ServicePort

	ports = append(ports, v1.ServicePort{
		Name:       "pgsql",
		Port:       int32(5432),
		TargetPort: intstr.IntOrString{IntVal: int32(5432)},
	})
	s.Spec.Type = "ExternalName"
	s.Spec.ExternalName = hostname

	s.Spec.Ports = ports
	s.Name = internalname
	s.Annotations = map[string]string{"origin": "rds"}
	s.Namespace = namespace
	return s
}

// CreateService Creates or updates a service in Kubernetes with the new information
func (r *RDS) CreateService(ctx context.Context, namespace string, hostname string, internalname string) error {

	// create a service in kubernetes that points to the AWS RDS instance
	kubectl, err := kube.Client()
	if err != nil {
		return err
	}
	serviceInterface := kubectl.CoreV1().Services(namespace)

	s, sErr := serviceInterface.Get(ctx, hostname, metav1.GetOptions{})

	create := false
	if sErr != nil {
		s = &v1.Service{}
		create = true
	}
	s = r.createServiceObj(s, namespace, hostname, internalname)
	if create {
		_, err = serviceInterface.Create(ctx, s, metav1.CreateOptions{})
	} else {
		_, err = serviceInterface.Update(ctx, s, metav1.UpdateOptions{})
	}

	return err
}

func (r *RDS) DeleteService(ctx context.Context, namespace string, dbname string) error {
	log.Printf("DeleteService function called for %v database in namespace %v. This function returns nil until the EKS migration is in progress.", dbname, namespace)
	return nil

	/*
		TODO: We are about to migrate services to EKS which involves deploying helm charts to EKS and later removing the charts
		from the legacy cluster. It can happen that when charts are removed from the legacy cluster, this operator would
		delete a Redis instance in AWS (because the the chart does not have db.Spec.DeleteProtection). Therefore, to completely
		eliminate any possibilities of this happening, this delete function does not do anything.

		The migration should be finished by mid 2022. Check with Platform Ops for the status.
	*/
	//kubectl, err := kube.Client()
	//if err != nil {
	//	return err
	//}
	//serviceInterface := kubectl.CoreV1().Services(namespace)
	//err = serviceInterface.Delete(ctx, dbname, metav1.DeleteOptions{})
	//if err != nil {
	//	log.Println(err)
	//	return errors.Wrap(err, fmt.Sprintf("delete of service %v failed in namespace %v", dbname, namespace))
	//}
	//return nil
}

func (r *RDS) GetSecret(ctx context.Context, namespace string, name string, key string) (string, error) {
	kubectl, err := kube.Client()
	if err != nil {
		return "", err
	}
	secret, err := kubectl.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", errors.Wrap(err, fmt.Sprintf("unable to fetch secret %v", name))
	}
	password := secret.Data[key]
	return string(password), nil
}
