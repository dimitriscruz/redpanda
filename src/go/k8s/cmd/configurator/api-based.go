// Copyright 2021 Vectorized, Inc.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.md
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0

package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	redpandav1alpha1 "github.com/vectorizedio/redpanda/src/go/k8s/apis/redpanda/v1alpha1"
	"github.com/vectorizedio/redpanda/src/go/rpk/pkg/config"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func createClientSet() (*kubernetes.Clientset, error) {
	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to create in cluster config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create clientset: %w", err)
	}
	return clientset, nil
}

func getNode(
	clientset *kubernetes.Clientset, nodeName string,
) (*corev1.Node, error) {
	node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve node: %w", err)
	}
	return node, nil
}

func getNodeportService(
	clientset *kubernetes.Clientset, key types.NamespacedName,
) (*corev1.Service, error) {
	svc, err := clientset.CoreV1().Services(key.Namespace).Get(context.Background(), key.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve node port services %w", err)
	}
	return svc, nil
}

func getRedpandaClusterCR(
	clusterKey client.ObjectKey,
) (*redpandav1alpha1.Cluster, error) {
	// Create client for redpanda cluster
	scheme := runtime.NewScheme()
	err := redpandav1alpha1.AddToScheme(scheme)
	if err != nil {
		return nil, err
	}
	kubeconfig, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	cl, err := client.New(kubeconfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve redpanda cluster: %w", err)
	}

	var cluster redpandav1alpha1.Cluster
	err = cl.Get(context.Background(), clusterKey, &cluster)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve redpanda cluster: %w", err)
	}
	return &cluster, nil
}

func registerAdvertisedKafkaAPIUsingK8sAPI(
	c *configuratorConfig, cfg *config.Config, index brokerID,
) error {
	kafkaAPIPort, err := getInternalKafkaAPIPort(cfg)
	if err != nil {
		log.Fatal(err)
	}
	cfg.Redpanda.AdvertisedKafkaApi = []config.NamedSocketAddress{
		{
			SocketAddress: config.SocketAddress{
				Address: c.hostName + "." + c.svcFQDN,
				Port:    kafkaAPIPort,
			},
			Name: internalKafkaListenerPortName,
		},
	}

	clientset, err := createClientSet()
	if err != nil {
		return fmt.Errorf("unable to create clientset: %w", err)
	}

	cluster, err := getRedpandaClusterCR(types.NamespacedName{Namespace: c.podNamespace, Name: clusterName(c.hostName)})
	if err != nil {
		return fmt.Errorf("unable to get redpanda cluster CR: %w", err)
	}

	svcKey := types.NamespacedName{Name: clusterName(c.hostName) + "-external", Namespace: c.podNamespace}
	for _, listener := range cluster.Spec.Configuration.KafkaAPI {
		if !listener.External.Enabled || listener.External.Subdomain == "" {
			continue
		}
		svc, err := getNodeportService(clientset, svcKey)
		if err != nil {
			log.Fatalf("%s", fmt.Errorf("unable to get nodeport service: %w", err))
		}
		hostPort, err := getExternalPort(svc.Spec.Ports)
		if err != nil {
			return err
		}
		cfg.Redpanda.AdvertisedKafkaApi = append(cfg.Redpanda.AdvertisedKafkaApi, config.NamedSocketAddress{
			SocketAddress: config.SocketAddress{
				Address: fmt.Sprintf("%d.%s", index, listener.External.Subdomain),
				Port:    hostPort,
			},
			Name: externalKafkaListenerPortName,
		})
		return nil
	}

	for _, listener := range cluster.Spec.Configuration.KafkaAPI {
		if !listener.External.Enabled {
			continue
		}
		node, err := getNode(clientset, c.nodeName)
		if err != nil {
			log.Fatalf("%s", fmt.Errorf("unable to get the node: %w", err))
		}
		svc, err := getNodeportService(clientset, svcKey)
		if err != nil {
			log.Fatalf("%s", fmt.Errorf("unable to get nodeport service: %w", err))
		}

		hostPort, err := getExternalPort(svc.Spec.Ports)
		if err != nil {
			return err
		}
		cfg.Redpanda.AdvertisedKafkaApi = append(cfg.Redpanda.AdvertisedKafkaApi, config.NamedSocketAddress{
			SocketAddress: config.SocketAddress{
				Address: getExternalIP(node),
				Port:    hostPort,
			},
			Name: externalKafkaListenerPortName,
		})
		return nil
	}

	return nil
}

func getExternalPort(ports []corev1.ServicePort) (int, error) {
	var hostPort int = 0
	for _, port := range ports {
		if port.Name == externalKafkaListenerPortName {
			hostPort = int(port.NodePort)
		}
	}
	if hostPort == 0 {
		return 0, errorExternalPortMissing
	}
	return hostPort, nil
}

func clusterName(hostName string) string {
	s := strings.Split(hostName, "-")
	last := len(s) - 1
	return strings.Join(s[0:last], "-")
}
