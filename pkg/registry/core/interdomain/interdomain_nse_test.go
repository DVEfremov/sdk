// Copyright (c) 2020-2021 Doc.ai and/or its affiliates.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package interdomain_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/grpc"

	registryapi "github.com/networkservicemesh/api/pkg/api/registry"

	"github.com/networkservicemesh/sdk/pkg/registry"
	"github.com/networkservicemesh/sdk/pkg/registry/common/memory"
	"github.com/networkservicemesh/sdk/pkg/registry/common/setid"
	registrychain "github.com/networkservicemesh/sdk/pkg/registry/core/chain"
	"github.com/networkservicemesh/sdk/pkg/tools/grpcutils"
	"github.com/networkservicemesh/sdk/pkg/tools/sandbox"
)

/*
	TestInterdomainNetworkServiceEndpointRegistry covers the next scenario:
		1. local registry from domain2 has entry "ns-1"
		2. nsmgr from domain1 call find with query "nse-1@domain2"
		3. local registry proxies query to proxy registry
		4. proxy registry proxies query to local registry from domain2
	Expected: nsmgr found ns
	domain1                                      domain2
	 ___________________________________         ___________________
	|                                   | Find  |                   |
	| local registry --> proxy registry | ----> | local registry    |
	|                                   |       |                   |
	____________________________________         ___________________
*/
func TestInterdomainNetworkServiceEndpointRegistry(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	const remoteRegistryDomain = "domain2.local.registry"

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	dnsServer := new(sandbox.FakeDNSResolver)

	domain1 := sandbox.NewBuilder(t).
		SetContext(ctx).
		SetNodesCount(0).
		SetDNSResolver(dnsServer).
		Build()

	domain2 := sandbox.NewBuilder(t).
		SetContext(ctx).
		SetNodesCount(0).
		SetDNSResolver(dnsServer).
		Build()

	require.NoError(t, dnsServer.Register(remoteRegistryDomain, domain2.Registry.URL))

	expirationTime, _ := ptypes.TimestampProto(time.Now().Add(time.Hour))

	reg, err := domain2.Registry.NetworkServiceEndpointRegistryServer().Register(
		context.Background(),
		&registryapi.NetworkServiceEndpoint{
			Name:           "nse-1",
			Url:            "nsmgr-url",
			ExpirationTime: expirationTime,
		},
	)
	require.Nil(t, err)

	cc, err := grpc.DialContext(ctx, grpcutils.URLToTarget(domain1.Registry.URL), grpc.WithBlock(), grpc.WithInsecure())
	require.Nil(t, err)

	defer func() {
		_ = cc.Close()
	}()

	client := registryapi.NewNetworkServiceEndpointRegistryClient(cc)

	stream, err := client.Find(ctx, &registryapi.NetworkServiceEndpointQuery{
		NetworkServiceEndpoint: &registryapi.NetworkServiceEndpoint{
			Name: reg.Name + "@" + remoteRegistryDomain,
		},
	})

	require.Nil(t, err)

	list := registryapi.ReadNetworkServiceEndpointList(stream)

	require.Len(t, list, 1)
	require.Equal(t, reg.Name+"@nsmgr-url", list[0].Name)
}

/*
	TestLocalDomain_NetworkServiceEndpointRegistry covers the next scenario:
		1. nsmgr from domain1 calls find with query "nse-1@domain1"
		2. local registry proxies query to proxy registry
		3. proxy registry proxies query to local registry removes interdomain symbol
		4. local registry finds nse-1 with local nsmgr URL

	Expected: nsmgr found nse
	domain1
	 ____________________________________
	|                                    |
	| local registry <--> proxy registry |
	|                                    |
	_____________________________________
*/
func TestLocalDomain_NetworkServiceEndpointRegistry(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	const localRegistryDomain = "domain1.local.registry"

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	dnsServer := new(sandbox.FakeDNSResolver)

	domain1 := sandbox.NewBuilder(t).
		SetContext(ctx).
		SetNodesCount(0).
		SetDNSDomainName(localRegistryDomain).
		SetDNSResolver(dnsServer).
		Build()

	require.NoError(t, dnsServer.Register(localRegistryDomain, domain1.Registry.URL))

	expirationTime, _ := ptypes.TimestampProto(time.Now().Add(time.Hour))

	reg, err := domain1.Registry.NetworkServiceEndpointRegistryServer().Register(
		context.Background(),
		&registryapi.NetworkServiceEndpoint{
			Name:           "nse-1",
			Url:            "test://publicNSMGRurl",
			ExpirationTime: expirationTime,
		},
	)
	require.Nil(t, err)

	cc, err := grpc.DialContext(ctx, grpcutils.URLToTarget(domain1.Registry.URL), grpc.WithBlock(), grpc.WithInsecure())
	require.Nil(t, err)
	defer func() {
		_ = cc.Close()
	}()

	client := registryapi.NewNetworkServiceEndpointRegistryClient(cc)

	stream, err := client.Find(context.Background(), &registryapi.NetworkServiceEndpointQuery{
		NetworkServiceEndpoint: &registryapi.NetworkServiceEndpoint{
			Name: reg.Name + "@" + localRegistryDomain,
		},
	})

	require.Nil(t, err)

	list := registryapi.ReadNetworkServiceEndpointList(stream)

	require.Len(t, list, 1)
	require.Equal(t, reg.Name, list[0].Name)
	require.Equal(t, "test://publicNSMGRurl", list[0].Url)
}

/*
	TestInterdomainFloatingNetworkServiceEndpointRegistry covers the next scenario:
		1. local registry from domain3 registers entry "ns-1"
		2. proxy registry from domain3 proxies entry "ns-1" to floating registry
		3. nsmgr from domain1 call find with query "nse-1@domain3"
		4. local registry from domain1 proxies query to proxy registry from domain1
		5. proxy registry from domain1 proxies query to floating registry
	Expected: nsmgr found ns
	domain1	                                        domain2                            domain3
	 ___________________________________            ___________________                ___________________________________
	|                                   | 2. Find  |                    | 1. Register |                                   |
	| local registry --> proxy registry | -------> | floating registry  | <---------  | proxy registry <-- local registry |
	|                                   |          |                    |             |                                   |
	____________________________________            ___________________                ___________________________________
*/

func TestInterdomainFloatingNetworkServiceEndpointRegistry(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	const remoteRegistryDomain = "domain3.local.registry"
	const remoteProxyRegistryDomain = "domain3.proxy.registry"
	const floatingRegistryDomain = "domain2.floating.registry"

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	dnsServer := new(sandbox.FakeDNSResolver)

	domain1 := sandbox.NewBuilder(t).
		SetContext(ctx).
		SetNodesCount(0).
		SetDNSResolver(dnsServer).
		Build()

	domain2 := sandbox.NewBuilder(t).
		SetContext(ctx).
		SetNodesCount(0).
		SetDNSResolver(dnsServer).
		Build()

	domain3 := sandbox.NewBuilder(t).
		SetNodesCount(0).
		SetRegistrySupplier(func(context.Context, time.Duration, *url.URL, ...grpc.DialOption) registry.Registry {
			return registry.NewServer(
				memory.NewNetworkServiceRegistryServer(),
				registrychain.NewNetworkServiceEndpointRegistryServer(
					memory.NewNetworkServiceEndpointRegistryServer(),
					setid.NewNetworkServiceEndpointRegistryServer(),
				),
			)
		}).
		SetRegistryProxySupplier(nil).
		Build()

	require.NoError(t, dnsServer.Register(remoteRegistryDomain, domain2.Registry.URL))
	require.NoError(t, dnsServer.Register(remoteProxyRegistryDomain, domain2.RegistryProxy.URL))
	require.NoError(t, dnsServer.Register(floatingRegistryDomain, domain3.Registry.URL))

	expirationTime, _ := ptypes.TimestampProto(time.Now().Add(time.Hour))

	reg, err := domain2.Registry.NetworkServiceEndpointRegistryServer().Register(
		context.Background(),
		&registryapi.NetworkServiceEndpoint{
			Name:           "nse-1@" + floatingRegistryDomain,
			Url:            "test://publicNSMGRurl",
			ExpirationTime: expirationTime,
		},
	)
	require.Nil(t, err)

	name := strings.Split(reg.Name, "@")[0]

	cc, err := grpc.DialContext(ctx, grpcutils.URLToTarget(domain1.Registry.URL), grpc.WithBlock(), grpc.WithInsecure())
	require.Nil(t, err)
	defer func() {
		_ = cc.Close()
	}()

	client := registryapi.NewNetworkServiceEndpointRegistryClient(cc)

	stream, err := client.Find(ctx, &registryapi.NetworkServiceEndpointQuery{
		NetworkServiceEndpoint: &registryapi.NetworkServiceEndpoint{
			Name: name + "@" + floatingRegistryDomain,
		},
	})

	require.Nil(t, err)

	list := registryapi.ReadNetworkServiceEndpointList(stream)

	require.Len(t, list, 1)
	require.Equal(t, name+"@test://publicNSMGRurl", list[0].Name)
}
