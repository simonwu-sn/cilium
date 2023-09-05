// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package srv6

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/cilium/cilium/enterprise/pkg/srv6/sidmanager"
	srv6Types "github.com/cilium/cilium/enterprise/pkg/srv6/types"
	"github.com/cilium/cilium/pkg/bgpv1/agent/signaler"
	"github.com/cilium/cilium/pkg/ebpf"
	"github.com/cilium/cilium/pkg/hive/hivetest"
	"github.com/cilium/cilium/pkg/identity"
	"github.com/cilium/cilium/pkg/ipam"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2alpha1"
	slimMetav1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	k8sTypes "github.com/cilium/cilium/pkg/k8s/types"
	"github.com/cilium/cilium/pkg/labels"
	"github.com/cilium/cilium/pkg/maps/srv6map"
	"github.com/cilium/cilium/pkg/option"
	"github.com/cilium/cilium/pkg/promise"
	"github.com/cilium/cilium/pkg/testutils"
	testidentity "github.com/cilium/cilium/pkg/testutils/identity"
	"github.com/cilium/cilium/pkg/types"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeSIDAllocator struct {
	sid           *srv6Types.SID
	behaviorType  srv6Types.BehaviorType
	allocatedSIDs []*sidmanager.SIDInfo
}

func (fsa *fakeSIDAllocator) Locator() *srv6Types.Locator {
	return nil
}

func (fsa *fakeSIDAllocator) BehaviorType() srv6Types.BehaviorType {
	return fsa.behaviorType
}

func (fsa *fakeSIDAllocator) Allocate(_ netip.Addr, owner string, metadata string, behavior srv6Types.Behavior) (*sidmanager.SIDInfo, error) {
	return &sidmanager.SIDInfo{
		Owner:        owner,
		MetaData:     metadata,
		SID:          fsa.sid,
		BehaviorType: fsa.behaviorType,
		Behavior:     behavior,
	}, nil
}

func (fsa *fakeSIDAllocator) AllocateNext(owner string, metadata string, behavior srv6Types.Behavior) (*sidmanager.SIDInfo, error) {
	return &sidmanager.SIDInfo{
		Owner:        owner,
		MetaData:     metadata,
		SID:          fsa.sid,
		BehaviorType: fsa.behaviorType,
		Behavior:     behavior,
	}, nil
}

func (fsa *fakeSIDAllocator) Release(sid netip.Addr) error {
	return nil
}

func (fsa *fakeSIDAllocator) AllocatedSIDs(owner string) []*sidmanager.SIDInfo {
	return fsa.allocatedSIDs
}

type fakeSIDManager struct {
	pools map[string]sidmanager.SIDAllocator
}

func (fsm *fakeSIDManager) ManageSID(poolName string, fn func(allocator sidmanager.SIDAllocator) (bool, error)) error {
	allocator, ok := fsm.pools[poolName]
	if !ok {
		return fmt.Errorf("pool doesn't exist")
	}
	_, err := fn(allocator)
	return err
}

func (fsm *fakeSIDManager) Subscribe(subscriberName string, subscriber sidmanager.SIDManagerSubscriber, done func()) {
	for poolName, allocator := range fsm.pools {
		subscriber.OnAddLocator(poolName, allocator)
	}
	done()
	return
}

type fakeIPAMAllocator struct {
	sid net.IP
}

func (fa *fakeIPAMAllocator) Allocate(ip net.IP, owner string, pool ipam.Pool) (*ipam.AllocationResult, error) {
	return nil, nil
}

func (fa *fakeIPAMAllocator) AllocateWithoutSyncUpstream(ip net.IP, owner string, pool ipam.Pool) (*ipam.AllocationResult, error) {
	return nil, nil
}

func (fa *fakeIPAMAllocator) Release(ip net.IP, pool ipam.Pool) error {
	return nil
}

func (fa *fakeIPAMAllocator) AllocateNext(owner string, pool ipam.Pool) (*ipam.AllocationResult, error) {
	return &ipam.AllocationResult{
		IP: fa.sid,
	}, nil
}

func (fa *fakeIPAMAllocator) AllocateNextWithoutSyncUpstream(owner string, pool ipam.Pool) (*ipam.AllocationResult, error) {
	return nil, nil
}

func (fa *fakeIPAMAllocator) Dump() (map[string]string, string) {
	return nil, ""
}

func (fa *fakeIPAMAllocator) RestoreFinished() {
	return
}

type comparableObject[T any] interface {
	metav1.Object
	DeepEqual(obj T) bool
}

func planK8sObj[T comparableObject[T]](oldObjs, newObjs []T) (toAdd, toUpdate, toDelete []T) {
	for _, newObj := range newObjs {
		found := false
		for _, oldObj := range oldObjs {
			if newObj.GetName() == oldObj.GetName() {
				found = true
				if !newObj.DeepEqual(oldObj) {
					toUpdate = append(toUpdate, newObj)
				}
				break
			}
		}
		if !found {
			toAdd = append(toAdd, newObj)
		}
	}
	for _, oldObj := range oldObjs {
		found := false
		for _, newObj := range newObjs {
			if oldObj.GetName() == newObj.GetName() {
				found = true
				break
			}
		}
		if !found {
			toDelete = append(toDelete, oldObj)
		}
	}
	return
}

type comparableKV[T any] interface {
	Equal(obj T) bool
}

type vrfKV struct {
	k *srv6map.VRFKey
	v *srv6map.VRFValue
}

func (a *vrfKV) Equal(b *vrfKV) bool {
	return a.k.Equal(b.k) && a.v.Equal(b.v)
}

type policyKV struct {
	k *srv6map.PolicyKey
	v *srv6map.PolicyValue
}

func (a *policyKV) Equal(b *policyKV) bool {
	return a.k.Equal(b.k) && a.v.Equal(b.v)
}

type sidKV struct {
	k *srv6map.SIDKey
	v *srv6map.SIDValue
}

func (a *sidKV) Equal(b *sidKV) bool {
	return a.k.Equal(b.k) && a.v.Equal(b.v)
}

func bpfMapsEqual[T comparableKV[T]](a, b []T) bool {
	for _, kva := range a {
		found := false
		for _, kvb := range a {
			if kva.Equal(kvb) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	for _, kvb := range b {
		found := false
		for _, kva := range a {
			if kvb.Equal(kva) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

func TestSRv6Manager(t *testing.T) {
	testutils.PrivilegedTest(t)

	log.Logger.SetLevel(logrus.DebugLevel)

	// Fixtures
	endpoint1 := &k8sTypes.CiliumEndpoint{
		ObjectMeta: slimMetav1.ObjectMeta{
			Namespace: "default",
			Name:      "pod1",
			Labels: map[string]string{
				"vrf": "vrf0",
			},
		},
		Identity: &v2.EndpointIdentity{
			Labels: []string{
				"k8s:vrf=vrf0",
			},
		},
		Networking: &v2.EndpointNetworking{
			Addressing: v2.AddressPairList{
				{
					IPV4: "10.0.0.1",
				},
			},
		},
	}

	ip1 := net.ParseIP("10.0.0.1")
	_, cidr1, _ := net.ParseCIDR("0.0.0.0/0")
	_, cidr2, _ := net.ParseCIDR("10.0.0.0/24")

	sid1IP := net.ParseIP("fd00:0:0:1::")
	sid2IP := net.ParseIP("fd00:0:1:1::")
	sid3 := srv6Types.MustNewSID(
		netip.MustParseAddr("fd00:0:1:2::"),
		srv6Types.MustNewSIDStructure(32, 16, 16, 0),
	)

	vrf0 := &v2alpha1.CiliumSRv6VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vrf0",
		},
		Spec: v2alpha1.CiliumSRv6VRFSpec{
			VRFID: 1,
			Rules: []v2alpha1.VRFRule{
				{
					Selectors: []v2alpha1.EgressRule{
						{
							PodSelector: &slimMetav1.LabelSelector{
								MatchLabels: map[string]slimMetav1.MatchLabelsValue{
									"vrf": "vrf0",
								},
							},
						},
					},
					DestinationCIDRs: []v2alpha1.CIDR{
						v2alpha1.CIDR(cidr1.String()),
					},
				},
			},
		},
	}

	vrf0WithVRFID2 := vrf0.DeepCopy()
	vrf0WithVRFID2.Spec.VRFID = 2

	vrf0WithDestinationCIDR := vrf0.DeepCopy()
	vrf0WithDestinationCIDR.Spec.Rules[0].DestinationCIDRs[0] = v2alpha1.CIDR(cidr2.String())

	vrf0WithExportRouteTarget := vrf0.DeepCopy()
	vrf0WithExportRouteTarget.Spec.ExportRouteTarget = "65000:1"

	vrf0WithExportRouteTarget2 := vrf0.DeepCopy()
	vrf0WithExportRouteTarget2.Spec.ExportRouteTarget = "65000:2"

	vrf0WithExportRouteTargetAndLocatorPoolRef := vrf0WithExportRouteTarget.DeepCopy()
	vrf0WithExportRouteTargetAndLocatorPoolRef.Spec.LocatorPoolRef = "pool1"

	policy0 := &v2alpha1.CiliumSRv6EgressPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "policy0",
		},
		Spec: v2alpha1.CiliumSRv6EgressPolicySpec{
			VRFID: 1,
			DestinationCIDRs: []v2alpha1.CIDR{
				v2alpha1.CIDR(cidr2.String()),
			},
			DestinationSID: sid1IP.String(),
		},
	}

	policy0WithVRFID2 := policy0.DeepCopy()
	policy0WithVRFID2.Spec.VRFID = 2

	tests := []struct {
		name                    string
		initEndpoints           []*k8sTypes.CiliumEndpoint
		initVRFs                []*v2alpha1.CiliumSRv6VRF
		initPolicies            []*v2alpha1.CiliumSRv6EgressPolicy
		initVRFMapEntries       []*vrfKV
		initPolicyMapEntries    []*policyKV
		initSIDMapEntries       []*sidKV
		updatedEndpoints        []*k8sTypes.CiliumEndpoint
		updatedVRFs             []*v2alpha1.CiliumSRv6VRF
		updatedPolicies         []*v2alpha1.CiliumSRv6EgressPolicy
		updatedVRFMapEntries    []*vrfKV
		updatedPolicyMapEntries []*policyKV
		updatedSIDMapEntries    []*sidKV
	}{
		{
			name:             "Add VRF",
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
		},
		{
			name:          "Update VRF VRFID",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithVRFID2},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 2},
				},
			},
		},
		{
			name:          "Update VRF DestinationCIDR",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithDestinationCIDR},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr2},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
		},
		{
			name:          "Update VRF ExportRouteTarget",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTarget},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTarget2},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr2},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
		},
		{
			name:             "Allocate SID with default allocator",
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTarget},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid2IP.To16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
		},
		{
			name:          "Remove VRF ExportRouteTarget",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTarget},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			initSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid2IP.To16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
		},
		{
			name:             "Allocate SID with SIDManager",
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTargetAndLocatorPoolRef},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid3.As16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
		},
		{
			name:          "Update SID allocation from default allocator to SIDManager",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTarget},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			initSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid2IP.To16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTargetAndLocatorPoolRef},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid3.As16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
		},
		{
			name:          "Update SID allocation from SIDManager to default allocator",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTargetAndLocatorPoolRef},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			initSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid3.As16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0WithExportRouteTarget},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedSIDMapEntries: []*sidKV{
				{
					k: &srv6map.SIDKey{SID: types.IPv6(sid2IP.To16())},
					v: &srv6map.SIDValue{VRFID: 1},
				},
			},
		},
		{
			name:          "Delete VRF",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
		},
		{
			name:             "Add Endpoint",
			initVRFs:         []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
		},
		{
			name:          "Delete Endpoint",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedVRFs: []*v2alpha1.CiliumSRv6VRF{vrf0},
		},
		{
			name:             "Create Policy",
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedPolicies:  []*v2alpha1.CiliumSRv6EgressPolicy{policy0},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedPolicyMapEntries: []*policyKV{
				{
					k: &srv6map.PolicyKey{VRFID: 1, DestCIDR: cidr2},
					v: &srv6map.PolicyValue{SID: types.IPv6(sid1IP.To16())},
				},
			},
		},
		{
			name:          "Update Policy VRFID",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			initPolicies:  []*v2alpha1.CiliumSRv6EgressPolicy{policy0},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			initPolicyMapEntries: []*policyKV{
				{
					k: &srv6map.PolicyKey{VRFID: 1, DestCIDR: cidr2},
					v: &srv6map.PolicyValue{SID: types.IPv6(sid1IP.To16())},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedPolicies:  []*v2alpha1.CiliumSRv6EgressPolicy{policy0WithVRFID2},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			updatedPolicyMapEntries: []*policyKV{
				{
					k: &srv6map.PolicyKey{VRFID: 2, DestCIDR: cidr2},
					v: &srv6map.PolicyValue{SID: types.IPv6(sid1IP.To16())},
				},
			},
		},
		{
			name:          "Delete Policy",
			initEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			initVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			initPolicies:  []*v2alpha1.CiliumSRv6EgressPolicy{policy0},
			initVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
			initPolicyMapEntries: []*policyKV{
				{
					k: &srv6map.PolicyKey{VRFID: 1, DestCIDR: cidr2},
					v: &srv6map.PolicyValue{SID: types.IPv6(sid1IP.To16())},
				},
			},
			updatedEndpoints: []*k8sTypes.CiliumEndpoint{endpoint1},
			updatedVRFs:      []*v2alpha1.CiliumSRv6VRF{vrf0},
			updatedVRFMapEntries: []*vrfKV{
				{
					k: &srv6map.VRFKey{SourceIP: &ip1, DestCIDR: cidr1},
					v: &srv6map.VRFValue{ID: 1},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv6map.CreateMaps()
			defer srv6map.DeleteMaps()

			// This allocator always returns fixed SID for AllocateNext
			allocator := &fakeSIDAllocator{
				sid:          sid3,
				behaviorType: srv6Types.BehaviorTypeBase,
			}

			fsm := &fakeSIDManager{
				pools: map[string]sidmanager.SIDAllocator{
					"pool1": allocator,
				},
			}

			// We can resolve SIDManager immediately because the pool is ready
			resolver, promise := promise.New[sidmanager.SIDManager]()
			resolver.Resolve(fsm)

			// Channel to notify k8s cache sync
			cacheStatus := make(chan struct{})

			// Dummy identity allocator
			identityAllocator := testidentity.NewMockIdentityAllocator(nil)

			manager := NewSRv6Manager(Params{
				Lifecycle: hivetest.Lifecycle(t),
				DaemonConfig: &option.DaemonConfig{
					EnableSRv6: true,
				},
				Sig:                    signaler.NewBGPCPSignaler(),
				CacheIdentityAllocator: identityAllocator,
				CacheStatus:            cacheStatus,
				SIDManagerPromise:      promise,
			})

			// This allocator always returns fixed SID for AllocateNext
			manager.SetSIDAllocator(&fakeIPAMAllocator{
				sid: sid2IP,
			})

			// Emulate an initial resource sync. Since it's hard to emulate
			// the behavior of identity allocator, we populate epDataStore
			// directly here.
			// manager.epDataStore = test.endpoints

			for _, ep := range test.initEndpoints {
				id, _, err := identityAllocator.AllocateIdentity(
					context.TODO(),
					labels.NewLabelsFromModel(ep.Identity.Labels),
					false,
					identity.InvalidIdentity,
				)
				copiedEp := ep.DeepCopy()
				copiedEp.Identity.ID = int64(id.ID)
				require.NoError(t, err)
				manager.OnUpdateEndpoint(copiedEp)
			}

			for _, vrf := range test.initVRFs {
				v, err := ParseVRF(vrf)
				require.NoError(t, err)
				manager.OnAddSRv6VRF(*v)
			}

			for _, policy := range test.initPolicies {
				p, err := ParsePolicy(policy)
				require.NoError(t, err)
				manager.OnAddSRv6Policy(*p)
			}

			// Ensure all maps are initialized as expected
			currentVRFMapEntries := []*vrfKV{}
			srv6map.SRv6VRFMap4.IterateWithCallback4(func(k *srv6map.VRFKey, v *srv6map.VRFValue) {
				currentVRFMapEntries = append(currentVRFMapEntries, &vrfKV{k: k, v: v})
			})
			bpfMapsEqual(currentVRFMapEntries, test.initVRFMapEntries)

			currentPolicyMapEntries := []*policyKV{}
			srv6map.SRv6PolicyMap4.IterateWithCallback4(func(k *srv6map.PolicyKey, v *srv6map.PolicyValue) {
				currentPolicyMapEntries = append(currentPolicyMapEntries, &policyKV{k: k, v: v})
			})
			bpfMapsEqual(currentPolicyMapEntries, test.initPolicyMapEntries)

			currentSIDMapEntries := []*sidKV{}
			srv6map.SRv6SIDMap.IterateWithCallback(func(k *srv6map.SIDKey, v *srv6map.SIDValue) {
				currentSIDMapEntries = append(currentSIDMapEntries, &sidKV{k: k, v: v})
			})
			bpfMapsEqual(currentSIDMapEntries, test.initSIDMapEntries)

			// Sync done. Close synced channel.
			close(cacheStatus)

			// Wait until the SIDAllocator is set
			require.Eventually(t, func() bool {
				return manager.sidAllocatorIsSet()
			}, time.Second*3, time.Millisecond*100)

			// Do CRUD for Endpoints
			epsToAdd, epsToUpdate, epsToDel := planK8sObj(test.initEndpoints, test.updatedEndpoints)

			for _, ep := range append(epsToAdd, epsToUpdate...) {
				id, _, err := identityAllocator.AllocateIdentity(
					context.TODO(),
					labels.NewLabelsFromModel(ep.Identity.Labels),
					false,
					identity.InvalidIdentity,
				)
				copiedEp := ep.DeepCopy()
				copiedEp.Identity.ID = int64(id.ID)
				require.NoError(t, err)
				manager.OnUpdateEndpoint(copiedEp)
			}

			for _, ep := range epsToDel {
				manager.OnDeleteEndpoint(ep)
			}

			// Do CRUD for VRFs
			vrfsToAdd, vrfsToUpdate, vrfsToDel := planK8sObj(test.initVRFs, test.updatedVRFs)

			for _, vrf := range append(vrfsToAdd, vrfsToUpdate...) {
				v, err := ParseVRF(vrf)
				require.NoError(t, err)
				manager.OnAddSRv6VRF(*v)
			}

			for _, vrf := range vrfsToDel {
				v, err := ParseVRF(vrf)
				require.NoError(t, err)
				manager.OnDeleteSRv6VRF(v.id)
			}

			// Do CRUD for Policies
			policiesToAdd, policiesToUpdate, policiesToDel := planK8sObj(test.initPolicies, test.updatedPolicies)

			for _, policy := range append(policiesToAdd, policiesToUpdate...) {
				p, err := ParsePolicy(policy)
				require.NoError(t, err)
				manager.OnAddSRv6Policy(*p)
			}

			for _, policy := range policiesToDel {
				p, err := ParsePolicy(policy)
				require.NoError(t, err)
				manager.OnDeleteSRv6Policy(p.id)
			}

			// Make sure all maps are updated as expected
			currentVRFMapEntries = []*vrfKV{}
			srv6map.SRv6VRFMap4.IterateWithCallback4(func(k *srv6map.VRFKey, v *srv6map.VRFValue) {
				currentVRFMapEntries = append(currentVRFMapEntries, &vrfKV{k: k, v: v})
			})
			bpfMapsEqual(currentVRFMapEntries, test.updatedVRFMapEntries)

			currentPolicyMapEntries = []*policyKV{}
			srv6map.SRv6PolicyMap4.IterateWithCallback4(func(k *srv6map.PolicyKey, v *srv6map.PolicyValue) {
				currentPolicyMapEntries = append(currentPolicyMapEntries, &policyKV{k: k, v: v})
			})
			bpfMapsEqual(currentPolicyMapEntries, test.updatedPolicyMapEntries)

			currentSIDMapEntries = []*sidKV{}
			srv6map.SRv6SIDMap.IterateWithCallback(func(k *srv6map.SIDKey, v *srv6map.SIDValue) {
				currentSIDMapEntries = append(currentSIDMapEntries, &sidKV{k: k, v: v})
			})
			bpfMapsEqual(currentSIDMapEntries, test.updatedSIDMapEntries)
		})
	}
}

func TestSRv6ManagerWithSIDManager(t *testing.T) {
	testutils.PrivilegedTest(t)

	log.Logger.SetLevel(logrus.DebugLevel)

	vrf0 := &v2alpha1.CiliumSRv6VRF{
		ObjectMeta: metav1.ObjectMeta{
			Name: "vrf0",
		},
		Spec: v2alpha1.CiliumSRv6VRFSpec{
			VRFID:             1,
			ExportRouteTarget: "65000:1",
			LocatorPoolRef:    "pool1",
			Rules: []v2alpha1.VRFRule{
				{
					Selectors: []v2alpha1.EgressRule{
						{
							PodSelector: &slimMetav1.LabelSelector{
								MatchLabels: map[string]slimMetav1.MatchLabelsValue{
									"vrf": "vrf0",
								},
							},
						},
					},
					DestinationCIDRs: []v2alpha1.CIDR{
						v2alpha1.CIDR("0.0.0.0/0"),
					},
				},
			},
		},
	}

	sid1 := srv6Types.MustNewSID(
		netip.MustParseAddr("fd00:0:1:1::"),
		srv6Types.MustNewSIDStructure(32, 16, 16, 0),
	)
	sid2 := srv6Types.MustNewSID(
		netip.MustParseAddr("fd00:0:1:2::"),
		srv6Types.MustNewSIDStructure(32, 16, 16, 0),
	)

	srv6map.CreateMaps()
	defer srv6map.DeleteMaps()

	fsm := &fakeSIDManager{
		// Start from an empty.
		pools: map[string]sidmanager.SIDAllocator{},
	}

	resolver, promise := promise.New[sidmanager.SIDManager]()

	// We can resolve SIDManager immediately because the pool is ready
	resolver.Resolve(fsm)

	// Dummy channel to notify k8s cache sync
	cacheStatus := make(chan struct{})

	manager := NewSRv6Manager(Params{
		Lifecycle: hivetest.Lifecycle(t),
		DaemonConfig: &option.DaemonConfig{
			EnableSRv6: true,
		},
		Sig:                    signaler.NewBGPCPSignaler(),
		CacheIdentityAllocator: nil,
		CacheStatus:            cacheStatus,
		SIDManagerPromise:      promise,
	})

	// This allocator will never be used
	manager.SetSIDAllocator(&fakeIPAMAllocator{})

	// Emulate initial resource sync. Since it's hard to emulate
	// the behavior of identity allocator, we populate epDataStore
	// directly here.
	manager.epDataStore = map[endpointID]*endpointMetadata{
		{Name: "pod1"}: {
			labels: map[string]string{
				"vrf": "vrf0",
			},
			id: endpointID{Name: "pod1"},
			ips: []net.IP{
				net.ParseIP("10.0.0.1"),
			},
		},
	}

	// Emulate an initial sync
	v, err := ParseVRF(vrf0)
	require.NoError(t, err)
	manager.OnAddSRv6VRF(*v)

	// Sync done. Close synced channel.
	close(cacheStatus)

	// Wait until the SIDAllocator is set
	require.Eventually(t, func() bool {
		return manager.sidAllocatorIsSet()
	}, time.Second*3, time.Millisecond*100)

	allocator1 := &fakeSIDAllocator{
		sid:           sid1,
		behaviorType:  srv6Types.BehaviorTypeBase,
		allocatedSIDs: []*sidmanager.SIDInfo{},
	}

	allocator2 := &fakeSIDAllocator{
		sid:           sid2,
		behaviorType:  srv6Types.BehaviorTypeUSID,
		allocatedSIDs: []*sidmanager.SIDInfo{},
	}

	// At this point, VRF wants to have SID from locator pool "pool1", but
	// since the pool doesn't exist, it won't get any allocation.

	t.Run("Test OnAddLocator", func(t *testing.T) {
		// Now add a new SIDAllocator (locator)
		fsm.pools["pool1"] = allocator1
		manager.OnAddLocator("pool1", allocator1)

		// Now the SID allocation from SIDManager and update to the SIDMap should happen eventually
		require.Eventually(t, func() bool {
			vrfs := manager.GetAllVRFs()
			require.Len(t, vrfs, 1)

			if vrfs[0].SIDInfo == nil {
				return false
			}

			info := vrfs[0].SIDInfo
			require.Equal(t, ownerName, info.Owner)
			require.Equal(t, vrf0.Name, info.MetaData)
			require.Equal(t, *sid1, *info.SID)
			require.Equal(t, srv6Types.BehaviorTypeBase, info.BehaviorType)
			require.Equal(t, srv6Types.BehaviorEndDT4, info.Behavior)

			var val srv6map.SIDValue
			err := srv6map.SRv6SIDMap.Lookup(srv6map.SIDKey{SID: sid1.As16()}, &val)
			return err == nil
		}, time.Second*3, time.Millisecond*100)
	})

	t.Run("Test OnUpdateLocator", func(t *testing.T) {
		// Update locator's behaviorType and trigger SID reallocation
		fsm.pools["pool1"] = allocator2
		manager.OnUpdateLocator("pool1", allocator1, allocator2)

		// Now the SID allocation from SIDManager should happen and old SIDMap entry should
		// be removed and a new SIDMap entry should appear.
		require.Eventually(t, func() bool {
			vrfs := manager.GetAllVRFs()
			require.Len(t, vrfs, 1)

			if vrfs[0].SIDInfo == nil {
				return false
			}

			info := vrfs[0].SIDInfo
			require.Equal(t, ownerName, info.Owner)
			require.Equal(t, vrf0.Name, info.MetaData)
			require.Equal(t, *sid2, *info.SID)
			require.Equal(t, srv6Types.BehaviorTypeUSID, info.BehaviorType)
			require.Equal(t, srv6Types.BehaviorUDT4, info.Behavior)

			var val srv6map.SIDValue
			err := srv6map.SRv6SIDMap.Lookup(srv6map.SIDKey{SID: sid2.As16()}, &val)
			if err != nil {
				return false
			}

			err = srv6map.SRv6SIDMap.Lookup(srv6map.SIDKey{SID: sid1.As16()}, &val)
			if err == nil {
				return false
			}

			return errors.Is(err, ebpf.ErrKeyNotExist)
		}, time.Second*3, time.Millisecond*100)
	})

	t.Run("Test OnDeleteLocator", func(t *testing.T) {
		// Delete locator
		delete(fsm.pools, "pool1")
		manager.OnDeleteLocator("pool1", allocator2)

		// Now the SID deletion from SIDManager should happen and old SIDMap entry should disappear
		require.Eventually(t, func() bool {
			vrfs := manager.GetAllVRFs()
			require.Len(t, vrfs, 1)

			if vrfs[0].SIDInfo != nil {
				return false
			}

			var val srv6map.SIDValue
			err = srv6map.SRv6SIDMap.Lookup(srv6map.SIDKey{SID: sid2.As16()}, &val)
			if err == nil {
				return false
			}

			return errors.Is(err, ebpf.ErrKeyNotExist)
		}, time.Second*3, time.Millisecond*100)
	})
}

func TestSIDManagerSIDRestoration(t *testing.T) {
	testutils.PrivilegedTest(t)

	log.Logger.SetLevel(logrus.DebugLevel)

	tests := []struct {
		name                string
		vrf                 *v2alpha1.CiliumSRv6VRF
		existingAllocations []*sidmanager.SIDInfo
		behaviorType        srv6Types.BehaviorType
		expectedAllocation  *sidmanager.SIDInfo
	}{
		{
			name: "Valid restoration",
			vrf: &v2alpha1.CiliumSRv6VRF{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vrf0",
				},
				Spec: v2alpha1.CiliumSRv6VRFSpec{
					VRFID:             1,
					ExportRouteTarget: "65000:1",
					LocatorPoolRef:    "pool1",
				},
			},
			existingAllocations: []*sidmanager.SIDInfo{
				{
					Owner:    ownerName,
					MetaData: "vrf0",
					SID: srv6Types.MustNewSID(
						netip.MustParseAddr("fd00:0:0:1::"),
						srv6Types.MustNewSIDStructure(32, 16, 16, 0),
					),
					BehaviorType: srv6Types.BehaviorTypeBase,
					Behavior:     srv6Types.BehaviorEndDT4,
				},
			},
			behaviorType: srv6Types.BehaviorTypeBase,
			expectedAllocation: &sidmanager.SIDInfo{
				Owner:    ownerName,
				MetaData: "vrf0",
				SID: srv6Types.MustNewSID(
					netip.MustParseAddr("fd00:0:0:1::"),
					srv6Types.MustNewSIDStructure(32, 16, 16, 0),
				),
				BehaviorType: srv6Types.BehaviorTypeBase,
				Behavior:     srv6Types.BehaviorEndDT4,
			},
		},
		{
			name: "VRF doesn't exist",
			vrf:  nil,
			existingAllocations: []*sidmanager.SIDInfo{
				{
					Owner:    ownerName,
					MetaData: "vrf0",
					SID: srv6Types.MustNewSID(
						netip.MustParseAddr("fd00:0:0:1::"),
						srv6Types.MustNewSIDStructure(32, 16, 16, 0),
					),
					BehaviorType: srv6Types.BehaviorTypeBase,
					Behavior:     srv6Types.BehaviorEndDT4,
				},
			},
			behaviorType:       srv6Types.BehaviorTypeBase,
			expectedAllocation: nil,
		},
		{
			name: "No ExportRouteTarget",
			vrf: &v2alpha1.CiliumSRv6VRF{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vrf0",
				},
				Spec: v2alpha1.CiliumSRv6VRFSpec{
					VRFID: 1,
				},
			},
			existingAllocations: []*sidmanager.SIDInfo{
				{
					Owner:    ownerName,
					MetaData: "vrf0",
					SID: srv6Types.MustNewSID(
						netip.MustParseAddr("fd00:0:0:1::"),
						srv6Types.MustNewSIDStructure(32, 16, 16, 0),
					),
					BehaviorType: srv6Types.BehaviorTypeBase,
					Behavior:     srv6Types.BehaviorEndDT4,
				},
			},
			behaviorType:       srv6Types.BehaviorTypeBase,
			expectedAllocation: nil,
		},
		{
			name: "LocatorPoolRef changed",
			vrf: &v2alpha1.CiliumSRv6VRF{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vrf0",
				},
				Spec: v2alpha1.CiliumSRv6VRFSpec{
					VRFID:             1,
					ExportRouteTarget: "65000:1",
					LocatorPoolRef:    "pool2",
				},
			},
			existingAllocations: []*sidmanager.SIDInfo{
				{
					Owner:    ownerName,
					MetaData: "vrf0",
					SID: srv6Types.MustNewSID(
						netip.MustParseAddr("fd00:0:0:1::"),
						srv6Types.MustNewSIDStructure(32, 16, 16, 0),
					),
					BehaviorType: srv6Types.BehaviorTypeBase,
					Behavior:     srv6Types.BehaviorEndDT4,
				},
			},
			behaviorType:       srv6Types.BehaviorTypeBase,
			expectedAllocation: nil,
		},
		{
			name: "Duplicated allocation",
			vrf: &v2alpha1.CiliumSRv6VRF{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vrf0",
				},
				Spec: v2alpha1.CiliumSRv6VRFSpec{
					VRFID:             1,
					ExportRouteTarget: "65000:1",
					LocatorPoolRef:    "pool1",
				},
			},
			existingAllocations: []*sidmanager.SIDInfo{
				{
					Owner:    ownerName,
					MetaData: "vrf0",
					SID: srv6Types.MustNewSID(
						netip.MustParseAddr("fd00:0:0:1::"),
						srv6Types.MustNewSIDStructure(32, 16, 16, 0),
					),
					BehaviorType: srv6Types.BehaviorTypeBase,
					Behavior:     srv6Types.BehaviorEndDT4,
				},
				{
					Owner:    ownerName,
					MetaData: "vrf0",
					SID: srv6Types.MustNewSID(
						netip.MustParseAddr("fd00:0:0:2::"),
						srv6Types.MustNewSIDStructure(32, 16, 16, 0),
					),
					BehaviorType: srv6Types.BehaviorTypeBase,
					Behavior:     srv6Types.BehaviorEndDT4,
				},
			},
			behaviorType: srv6Types.BehaviorTypeBase,
			expectedAllocation: &sidmanager.SIDInfo{
				Owner:    ownerName,
				MetaData: "vrf0",
				SID: srv6Types.MustNewSID(
					netip.MustParseAddr("fd00:0:0:1::"),
					srv6Types.MustNewSIDStructure(32, 16, 16, 0),
				),
				BehaviorType: srv6Types.BehaviorTypeBase,
				Behavior:     srv6Types.BehaviorEndDT4,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv6map.CreateMaps()
			defer srv6map.DeleteMaps()

			allocator := &fakeSIDAllocator{
				behaviorType:  test.behaviorType,
				allocatedSIDs: test.existingAllocations,
			}

			fsm := &fakeSIDManager{
				pools: map[string]sidmanager.SIDAllocator{
					"pool1": allocator,
				},
			}

			resolver, promise := promise.New[sidmanager.SIDManager]()

			// We can resolve SIDManager immediately because the pool is ready
			resolver.Resolve(fsm)

			// Dummy channel to notify k8s cache sync
			cacheStatus := make(chan struct{})

			manager := NewSRv6Manager(Params{
				Lifecycle: hivetest.Lifecycle(t),
				DaemonConfig: &option.DaemonConfig{
					EnableSRv6: true,
				},
				Sig:                    signaler.NewBGPCPSignaler(),
				CacheIdentityAllocator: nil,
				CacheStatus:            cacheStatus,
				SIDManagerPromise:      promise,
			})

			// This allocator will never be used
			manager.SetSIDAllocator(&fakeIPAMAllocator{})

			// Emulate an initial sync
			if test.vrf != nil {
				v, err := ParseVRF(test.vrf)
				require.NoError(t, err)
				manager.OnAddSRv6VRF(*v)
			}

			// Sync done. Close synced channel.
			close(cacheStatus)

			// Wait for the Subscribe call done. Restoration
			// happpens at this point.
			require.Eventually(t, func() bool {
				return manager.sidAllocatorIsSet()
			}, time.Second*3, time.Millisecond*100)

			require.Eventually(t, func() bool {
				vrfs := manager.GetAllVRFs()

				if test.vrf != nil {
					require.Len(t, vrfs, 1)
				} else {
					require.Len(t, vrfs, 0)
					return true
				}

				if test.expectedAllocation != nil {
					info := vrfs[0].SIDInfo
					expected := test.expectedAllocation
					require.Equal(t, expected.Owner, info.Owner)
					require.Equal(t, expected.MetaData, info.MetaData)
					require.Equal(t, *expected.SID, *info.SID)
					require.Equal(t, expected.BehaviorType, info.BehaviorType)
					require.Equal(t, expected.Behavior, info.Behavior)
				} else {
					require.Nil(t, vrfs[0].SIDInfo)
				}

				return true
			}, time.Second*3, time.Millisecond*100)
		})
	}
}
