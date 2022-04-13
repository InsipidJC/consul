package consul_cluster

import (
	"context"
	"fmt"
	"strings"

	consulnode "github.com/hashicorp/consul/integration/consul-container/libs/consul-node"
)

// Cluster abstract a Consul Cluster by providing
// a way to create and join a Consul Cluster
// a way to add nodes to a cluster
// a way to fetch the cluster leader...
type Cluster struct {
	Nodes []consulnode.ConsulNode
}

// New Create a new cluster based on the provided configuration
func New(configs []consulnode.Config) (*Cluster, error) {
	cluster := Cluster{}

	for _, c := range configs {
		n, err := consulnode.NewConsulContainer(context.Background(), c)
		if err != nil {
			return nil, err
		}
		cluster.Nodes = append(cluster.Nodes, n)
	}
	err := cluster.join()
	if err != nil {
		return nil, err
	}
	return &cluster, nil
}

// AddNodes add a number of nodes to the current cluster and join them to the cluster
func (c *Cluster) AddNodes(nodes []consulnode.ConsulNode) error {
	if len(c.Nodes) < 1 {
		return fmt.Errorf("cannot add a node to an empty cluster")
	}
	n0 := c.Nodes[0]
	for _, node := range nodes {
		addr, _ := n0.GetAddr()
		err := node.GetClient().Agent().Join(addr, false)
		if err != nil {
			return err
		}
		c.Nodes = append(c.Nodes, node)
	}
	return nil
}

// Terminate will attempt to terminate all the nodes in the cluster
// if a node termination fail, Terminate will abort and return and error
func (c *Cluster) Terminate() error {
	for _, n := range c.Nodes {
		err := n.Terminate()
		if err != nil {
			return err
		}
	}
	return nil
}

// Leader return the cluster leader node
// if no leader is available or the leader is not part of the cluster
// an error will be returned
func (c *Cluster) Leader() (consulnode.ConsulNode, error) {
	if len(c.Nodes) < 1 {
		return nil, fmt.Errorf("no node available")
	}
	n0 := c.Nodes[0]
	leaderAdd, err := n0.GetClient().Status().Leader()
	if err != nil {
		return nil, err
	}
	if leaderAdd == "" {
		return nil, fmt.Errorf("no leader available")
	}
	for _, n := range c.Nodes {
		addr, _ := n.GetAddr()
		if strings.Contains(leaderAdd, addr) {
			return n, nil
		}
	}
	return nil, fmt.Errorf("leader not found")
}

func (c *Cluster) join() error {
	if len(c.Nodes) < 2 {
		return nil
	}
	n0 := c.Nodes[0]
	for _, n := range c.Nodes {
		if n != n0 {
			addr, _ := n0.GetAddr()
			err := n.GetClient().Agent().Join(addr, false)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
