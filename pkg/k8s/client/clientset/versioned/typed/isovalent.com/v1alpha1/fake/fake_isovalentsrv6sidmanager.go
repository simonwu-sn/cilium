// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1alpha1 "github.com/cilium/cilium/pkg/k8s/apis/isovalent.com/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeIsovalentSRv6SIDManagers implements IsovalentSRv6SIDManagerInterface
type FakeIsovalentSRv6SIDManagers struct {
	Fake *FakeIsovalentV1alpha1
}

var isovalentsrv6sidmanagersResource = v1alpha1.SchemeGroupVersion.WithResource("isovalentsrv6sidmanagers")

var isovalentsrv6sidmanagersKind = v1alpha1.SchemeGroupVersion.WithKind("IsovalentSRv6SIDManager")

// Get takes name of the isovalentSRv6SIDManager, and returns the corresponding isovalentSRv6SIDManager object, and an error if there is any.
func (c *FakeIsovalentSRv6SIDManagers) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.IsovalentSRv6SIDManager, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootGetAction(isovalentsrv6sidmanagersResource, name), &v1alpha1.IsovalentSRv6SIDManager{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IsovalentSRv6SIDManager), err
}

// List takes label and field selectors, and returns the list of IsovalentSRv6SIDManagers that match those selectors.
func (c *FakeIsovalentSRv6SIDManagers) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.IsovalentSRv6SIDManagerList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootListAction(isovalentsrv6sidmanagersResource, isovalentsrv6sidmanagersKind, opts), &v1alpha1.IsovalentSRv6SIDManagerList{})
	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.IsovalentSRv6SIDManagerList{ListMeta: obj.(*v1alpha1.IsovalentSRv6SIDManagerList).ListMeta}
	for _, item := range obj.(*v1alpha1.IsovalentSRv6SIDManagerList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested isovalentSRv6SIDManagers.
func (c *FakeIsovalentSRv6SIDManagers) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewRootWatchAction(isovalentsrv6sidmanagersResource, opts))
}

// Create takes the representation of a isovalentSRv6SIDManager and creates it.  Returns the server's representation of the isovalentSRv6SIDManager, and an error, if there is any.
func (c *FakeIsovalentSRv6SIDManagers) Create(ctx context.Context, isovalentSRv6SIDManager *v1alpha1.IsovalentSRv6SIDManager, opts v1.CreateOptions) (result *v1alpha1.IsovalentSRv6SIDManager, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootCreateAction(isovalentsrv6sidmanagersResource, isovalentSRv6SIDManager), &v1alpha1.IsovalentSRv6SIDManager{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IsovalentSRv6SIDManager), err
}

// Update takes the representation of a isovalentSRv6SIDManager and updates it. Returns the server's representation of the isovalentSRv6SIDManager, and an error, if there is any.
func (c *FakeIsovalentSRv6SIDManagers) Update(ctx context.Context, isovalentSRv6SIDManager *v1alpha1.IsovalentSRv6SIDManager, opts v1.UpdateOptions) (result *v1alpha1.IsovalentSRv6SIDManager, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootUpdateAction(isovalentsrv6sidmanagersResource, isovalentSRv6SIDManager), &v1alpha1.IsovalentSRv6SIDManager{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IsovalentSRv6SIDManager), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeIsovalentSRv6SIDManagers) UpdateStatus(ctx context.Context, isovalentSRv6SIDManager *v1alpha1.IsovalentSRv6SIDManager, opts v1.UpdateOptions) (*v1alpha1.IsovalentSRv6SIDManager, error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootUpdateSubresourceAction(isovalentsrv6sidmanagersResource, "status", isovalentSRv6SIDManager), &v1alpha1.IsovalentSRv6SIDManager{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IsovalentSRv6SIDManager), err
}

// Delete takes name of the isovalentSRv6SIDManager and deletes it. Returns an error if one occurs.
func (c *FakeIsovalentSRv6SIDManagers) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewRootDeleteActionWithOptions(isovalentsrv6sidmanagersResource, name, opts), &v1alpha1.IsovalentSRv6SIDManager{})
	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeIsovalentSRv6SIDManagers) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewRootDeleteCollectionAction(isovalentsrv6sidmanagersResource, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.IsovalentSRv6SIDManagerList{})
	return err
}

// Patch applies the patch and returns the patched isovalentSRv6SIDManager.
func (c *FakeIsovalentSRv6SIDManagers) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.IsovalentSRv6SIDManager, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootPatchSubresourceAction(isovalentsrv6sidmanagersResource, name, pt, data, subresources...), &v1alpha1.IsovalentSRv6SIDManager{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.IsovalentSRv6SIDManager), err
}
