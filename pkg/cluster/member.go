// Copyright 2016 The etcd-operator Authors
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

package cluster

import (
	"fmt"
	"strings"

	api "github.com/coreos/etcd-operator/pkg/apis/etcd/v1beta2"
	"github.com/coreos/etcd-operator/pkg/util/etcdutil"
	"github.com/coreos/etcd/etcdserver/etcdserverpb"
	"github.com/pkg/errors"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Cluster) updateMembers(known etcdutil.MemberSet) error {
	resp, err := etcdutil.ListMembers(known.ClientURLs(), c.tlsConfig)
	if err != nil {
		return err
	}

	peerPodIPs := map[string]string{}
	if c.cluster.Spec.SelfHosted != nil {
		pods, err := c.config.KubeCli.CoreV1().Pods(c.cluster.Namespace).List(metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, p := range pods.Items {
			if p.Labels["etcd_cluster"] == c.cluster.Name {
				peerPodIPs[p.Labels["etcd_node"]] = p.Status.PodIP
			}
		}
	}

	members := etcdutil.MemberSet{}
	for _, m := range resp.Members {
		name, err := getMemberName(m, c.cluster.GetName(), c.cluster.Spec.SelfHosted)
		if err != nil {
			return errors.Wrap(err, "get member name failed")
		}
		ct, err := etcdutil.GetCounterFromMemberName(name)
		if err != nil {
			return newFatalError(fmt.Sprintf("get counter from member name (%s) failed: %v", name, err))
		}
		if ct+1 > c.memberCounter {
			c.memberCounter = ct + 1
		}

		member := &etcdutil.Member{
			Name:         name,
			Namespace:    c.cluster.Namespace,
			ID:           m.ID,
			SecurePeer:   c.isSecurePeer(),
			SecureClient: c.isSecureClient(),
		}

		if c.cluster.Spec.SelfHosted != nil {
			podIP, ok := peerPodIPs[m.Name]
			if !ok {
				return newFatalError(fmt.Sprintf("could not get podIP for %s member (ID %d)", m.Name, m.ID))
			}
			member.PodIP = podIP
			member.SelfHosted = true
		}

		members[name] = member
	}
	c.members = members
	return nil
}

func (c *Cluster) newMember(id int) *etcdutil.Member {
	name := etcdutil.CreateMemberName(c.cluster.Name, id)
	return &etcdutil.Member{
		Name:         name,
		Namespace:    c.cluster.Namespace,
		SecurePeer:   c.isSecurePeer(),
		SecureClient: c.isSecureClient(),
		SelfHosted:   c.cluster.Spec.SelfHosted != nil,
	}
}

func podsToMemberSet(pods []*v1.Pod, sc bool) etcdutil.MemberSet {
	members := etcdutil.MemberSet{}
	for _, pod := range pods {
		m := &etcdutil.Member{
			Name:         pod.Name,
			Namespace:    pod.Namespace,
			SecureClient: sc,
			PodIP:        pod.Status.PodIP,
		}
		members.Add(m)
	}
	return members
}

func getMemberName(m *etcdserverpb.Member, clusterName string, selfHosted *api.SelfHostedPolicy) (string, error) {
	if selfHosted != nil {
		if len(m.Name) == 0 || len(m.ClientURLs) == 0 {
			return "", newFatalError(fmt.Sprintf("unready self-hosted member (peerURL: %s)", m.PeerURLs[0]))
		}

		curl := m.ClientURLs[0]
		if curl == selfHosted.BootMemberClientEndpoint {
			return "", fmt.Errorf("skipping for self hosted cluster: waiting for the boot member (%s) to be removed...", m.Name)
		}

		if !strings.HasPrefix(m.Name, clusterName) {
			return "", newFatalError(fmt.Sprintf("member (%s) does not belong to this cluster", m.Name))
		}
		return m.Name, nil
	}

	name, err := etcdutil.MemberNameFromPeerURL(m.PeerURLs[0])
	if err != nil {
		return "", newFatalError(fmt.Sprintf("invalid member peerURL (%s): %v", m.PeerURLs[0], err))
	}
	return name, nil
}
