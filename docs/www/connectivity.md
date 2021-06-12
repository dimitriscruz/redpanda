---
title: Redpanda Operator - Connectivity
order: 0
---

# Redpanda Operator - Connectivity

The Redpanda operator supports internal and external connectivity. This document goes over the configuration needed, the main actions the operator takes to set up connectivity when a Cluster CR is created, and the expected outputs.

Before continuing it is recommended to go over the [Kubernetes Quick Start Guide](/docs/kubernetes-deployment) and follow the steps for setting up the Redpanda Operator and a Redpanda cluster.

## Connectivity within the Kubernetes cluster

### Cluster Custom Resource

The Cluster CR allows us to configure listeners per API, where each listener contains the desired port number. For example,

```
  configuration:
    kafkaApi:
    - port: 9092
    pandaproxyApi:
    - port: 8082
    adminApi:
    - port: 9644
```

The above example results in the creation of a headless Kubernetes Service. When using brokers, it is important to have direct access to individual brokers, hence the choice of a headless service where we don’t use a ClusterIP (clusterIP: None) that would otherwise load balance requests across the brokers. Instead, the clients contact brokers individually.

### Created Service

Creating a Redpanda cluster (through the operator) containing the above configuration results in the creation of a single Service having a port per listener:

```
Name:              three-node-cluster
..
Type:              ClusterIP
IP Families:       <none>
IP:                None
IPs:               <none>
Port:              admin  9644/TCP
TargetPort:        9644/TCP
Endpoints:         10.0.1.7:9644, ..
Port:              kafka  9092/TCP
TargetPort:        9092/TCP
Endpoints:         10.0.1.7:9092, ..
Port:              proxy  8082/TCP
TargetPort:        8082/TCP
Endpoints:         10.0.1.7:8082, ..
```

Each listener is given three fields, for example, the Kafka listener has a named port “kafka” with port 9092, as specified in the CR. It’s also given an identical target port that can be used by other processes in the internal network to communicate with, and finally an endpoint with the internal IP address of the service. For more information on Services, please check the official Kubernetes documentation: https://kubernetes.io/docs/concepts/services-networking/service/

### Redpanda configuration

In addition to setting up the service, the Redpanda operator prepares a configuration file `redpanda.yaml` for Redpanda consumption. The configuration contains the advertised addresses, which in this case are set to the FQDN of each broker.

```
$ kubectl logs three-node-cluster-0
..
advertised_kafka_api:
    - address: three-node-cluster-0.three-node-cluster.default.svc.cluster.local.
      name: kafka
      port: 9092
  ...
  kafka_api:
    - address: 0.0.0.0
      name: kafka
      port: 9092
```

### Cluster status

Similarly, the CR status contains the advertised internal addresses:

```
$ kubectl describe cluster three-node-cluster
..
Status:
  Nodes:
    Internal:
      three-node-cluster-0.three-node-cluster.default.svc.cluster.local.
      three-node-cluster-1.three-node-cluster.default.svc.cluster.local.
      three-node-cluster-2.three-node-cluster.default.svc.cluster.local.
```

### Trying it out

The configuration we went over provides internal connectivity. A client process in the same (internal) network can use the following addresses to communicate with the brokers using the Kafka API:

```
BROKERS=`kubectl get clusters three-node-cluster -o=jsonpath='{.status.nodes.internal}'  | jq -r 'join(",")'`
```

The result should hold the FQDN list:
```
three-node-cluster-0.three-node-cluster.default.svc.cluster.local.,
three-node-cluster-1.three-node-cluster.default.svc.cluster.local.,
three-node-cluster-2.three-node-cluster.default.svc.cluster.local.
```

Using rpk we can check the cluster info
```
rpk --brokers $BROKERS cluster info
```

The above example focuses on the Kafka API, however, the same holds with respect to connectivity for the Admin API and the Pandaproxy API: the internal addresses can be used by clients within the internal network.

## External connectivity

### Custom Resource
Now that we have verified that internal connectivity works, we can create a cluster that is externally accessible. The CR allows us to enable external connectivity by adding a second listener for each API with the “external” field enabled. A reason for having two listeners is to enable TLS/mTLS on the external one while keeping the internal one open. Let’s enable external connectivity for all supported APIs:

```
  configuration:
    kafkaApi:
     - port: 9092
     - external:
         enabled: true
    pandaproxyApi:
     - port: 8082
     - external:
         enabled: true
    adminApi:
    - port: 9644
     - external:
         enabled: true
```

### Created Services

The operator will create the headless service as earlier and an additional two services, 1) a load-balanced ClusterIP service that is used as an entrypoint for the Pandaproxy; 2) a Nodeport service used to expose each API to the node’s external network - note that if the node does not have external connectivity the APIs will not be reachable.

Each external listener is provided with a Nodeport that is automatically generated by Kubernetes. That is also the reason we do not provide a port to the external listeners in the spec. The advantage of that is collision prevention across host ports. Each nodeport has a corresponding internal port, which is set to the internal `port+1` and the naming convention of appending “-external” to the port name.

Let’s go over the two new services.

The `-cluster` Service is currently used by Pandaproxy as an entrypoint. It is a ClusterIP service, which means a request is load-balanced by Kubernetes to the Redpanda nodes with the help of a selector. As you can see from the service description below, in a 3-node Redpanda cluster we have three endpoints.

```
> kubectl describe svc external-connectivity-cluster

Name:              external-connectivity-cluster
..
Selector:          app.kubernetes.io/component=redpanda,app.kubernetes.io/instance=external-connectivity,app.kubernetes.io/name=redpanda
Type:              ClusterIP
IP Families:       <none>
IP:                10.3.246.143
IPs:               <none>
Port:              proxy-external  8083/TCP
TargetPort:        8083/TCP
Endpoints:         10.0.0.8:8083,10.0.1.8:8083,10.0.2.4:8083
```
The `-external` service is responsible for setting up nodeports for each API (with `external` enabled). In this case we have enabled external connectivity for all three APIs, hence in the description below we have three nodeports, each of them pointing to its target port (set to the original port + 1).

```
$ kubectl describe svc external-connectivity-external
Name:                     external-connectivity-external
..
Selector:                 <none>
Type:                     NodePort
IP Families:              <none>
IP:                       10.3.247.127
IPs:                      <none>

Port:                     kafka-external  9093/TCP
TargetPort:               9093/TCP
NodePort:                 kafka-external  31848/TCP
Endpoints:                <none>

Port:                     admin-external  9645/TCP
TargetPort:               9645/TCP
NodePort:                 admin-external  31490/TCP
Endpoints:                <none>

Port:                     proxy-external  8083/TCP
TargetPort:               8083/TCP
NodePort:                 proxy-external  30638/TCP
Endpoints:                <none>
```

The configuration we went over provides external connectivity. A client process in network with access to the host can use the following addresses to communicate with the brokers using the configured APIs. For accessing the Kafka API:

```
BROKERS=`kubectl get clusters external-connectivity -o=jsonpath='{.status.nodes.external}'  | jq -r 'join(",")'`
```

The result (BROKERS) should now contain a list of external IPs with the broker port:
`<node-0-external-ip>:31848,<node-1-external-ip>:31848,<node-2-external-ip>:31848`

Once we have the list of addresses we can use rpk or a Kafka client to test the connection, for example,
```
rpk --brokers $BROKERS cluster info
```

The above walk-through focuses on the Kafka API, however, similar steps can be followed the Admin API and the Pandaproxy API, and importantly the internal addresses can only be used by clients within the internal network.

### Redpanda configuration

Similarly to the “internal” listener case, the Redpanda operator prepares a configuration file `redpanda.yaml`, which in the case of external connectivity sets an additional advertised address per exposed API that now points to the external IP of each node and the nodeport of that API, for example,

```
  advertised_kafka_api:
    - address: external-connectivity-0.external-connectivity.default.svc.cluster.local.
      name: kafka
      port: 9092
    - address: <external-node-ip>
      name: kafka-external
      port: 31848
```

### Container configuration

The Redpanda operator ensures that the Redpanda container exposes the requested ports, as described in the CR. For example, when enabling external connectivity across APIs, the Redpanda containers are configured to expose two ports per API - one internal and one external. The operator does not create an external port for RPC - we use a single internal listener in that case.

Running `kubectl describe pod external-connectivity-0` we can see the ports and the mapping to the host ports created through a Nodeport Service:

```
Containers:
  redpanda:
    ..
    Ports:         33145/TCP, 9644/TCP, 9092/TCP, 8082/TCP, 9093/TCP, 9645/TCP, 8083/TCP
    Host Ports:    0/TCP, 0/TCP, 0/TCP, 0/TCP, 31848/TCP, 31490/TCP, 30638/TCP
```

### Port Summary - Configuration based on example specification

|   | Internal listener ports  | External listener ports (port+1:nodeport)  |
|---|---|---|
| Admin API  | 9644  | 9645:31490  |
| Kafka API  | 9092  | 9093:31848  |
| Pandaproxy API  | 8082  | 8083:30638  |e


### Using names instead of external IPs

The CRD includes a field “subdomain” that allows to specify the advertised address of external listeners. Here’s an example for the Kafka API:
```
  configuration:
    kafkaApi:
    - port: 9092
    - external:
        enabled: true
        subdomain: "test.subdomain.com"
```

The generated `redpanda.yaml` configuration uses the subdomain field to generate the advertised addresses for the external listeners following this format: <broker_id>.<subdomain>:<node_port>. Note that the DNS configuration is *not* handled by the Redpanda operator.

The Redpanda configuration will reflect this in the advertised addresses: 

```
redpanda:
  advertised_kafka_api:
    - address: external-connectivity-0.external-connectivity.default.svc.cluster.local.
      name: kafka
      port: 9092
    - address: 0.test.subdomain.com
      name: kafka-external
      port: 31631
```

Finally, the CR will contain the addresses in its status:

```
Status:
  Nodes:
    External:
      0.test.subdomain.com:31631
      1.test.subdomain.com:31631
      2.test.subdomain.com:31631
```


