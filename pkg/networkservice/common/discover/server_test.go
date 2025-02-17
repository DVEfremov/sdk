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

// package discover_test contains tests for package 'discover'
package discover_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/api/pkg/api/networkservice/payload"
	"github.com/networkservicemesh/api/pkg/api/registry"

	"github.com/networkservicemesh/sdk/pkg/networkservice/common/discover"
	"github.com/networkservicemesh/sdk/pkg/networkservice/core/next"
	"github.com/networkservicemesh/sdk/pkg/networkservice/utils/checks/checkcontext"
	"github.com/networkservicemesh/sdk/pkg/registry/common/memory"
	"github.com/networkservicemesh/sdk/pkg/registry/common/setid"
	"github.com/networkservicemesh/sdk/pkg/registry/core/adapters"
	registrynext "github.com/networkservicemesh/sdk/pkg/registry/core/next"
	"github.com/networkservicemesh/sdk/pkg/tools/clienturlctx"
)

func endpoints() []*registry.NetworkServiceEndpoint {
	ns := networkServiceName()
	return []*registry.NetworkServiceEndpoint{
		{
			NetworkServiceNames: []string{ns},
			NetworkServiceLabels: labels(ns,
				map[string]string{
					"app": "firewall",
				},
			),
		},
		{
			NetworkServiceNames: []string{ns},
			NetworkServiceLabels: labels(ns,
				map[string]string{
					"app": "some-middle-app",
				},
			),
		},
		{
			NetworkServiceNames: []string{ns},
			NetworkServiceLabels: labels(ns,
				map[string]string{
					"app": "vpn-gateway",
				},
			),
		},
	}
}

func networkServiceName() string {
	return "secure-intranet-connectivity"
}

func labels(service string, source map[string]string) map[string]*registry.NetworkServiceLabels {
	return map[string]*registry.NetworkServiceLabels{
		service: {
			Labels: source,
		},
	}
}

func fromAnywhereMatch() *registry.Match {
	return &registry.Match{
		SourceSelector: map[string]string{},
		Routes: []*registry.Destination{
			{
				DestinationSelector: map[string]string{
					"app": "firewall",
				},
			},
		},
	}
}

func fromFirewallMatch() *registry.Match {
	return &registry.Match{
		SourceSelector: map[string]string{
			"app": "firewall",
		},
		Routes: []*registry.Destination{
			{
				DestinationSelector: map[string]string{
					"app": "some-middle-app",
				},
			},
		},
	}
}

func fromSomeMiddleAppMatch() *registry.Match {
	return &registry.Match{
		SourceSelector: map[string]string{
			"app": "some-middle-app",
		},
		Routes: []*registry.Destination{
			{
				DestinationSelector: map[string]string{
					"app": "vpn-gateway",
				},
			},
		},
	}
}

func testServers(
	t *testing.T,
	nsName string,
	nses []*registry.NetworkServiceEndpoint,
	matches ...*registry.Match,
) (registry.NetworkServiceRegistryServer, registry.NetworkServiceEndpointRegistryServer) {
	nsServer := memory.NewNetworkServiceRegistryServer()
	if nsName != "" {
		_, err := nsServer.Register(context.Background(), &registry.NetworkService{
			Name:    nsName,
			Matches: matches,
		})
		require.NoError(t, err)
	}

	nseServer := registrynext.NewNetworkServiceEndpointRegistryServer(
		memory.NewNetworkServiceEndpointRegistryServer(),
		setid.NewNetworkServiceEndpointRegistryServer(),
	)
	for i, nse := range nses {
		var err error
		nses[i], err = nseServer.Register(context.Background(), nse)
		require.NoError(t, err)
	}

	return nsServer, nseServer
}

func TestMatchEmptySourceSelector(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()

	nsServer, nseServer := testServers(t, nsName, endpoints(), fromFirewallMatch(), fromSomeMiddleAppMatch(), fromAnywhereMatch())

	want := labels(nsName, map[string]string{
		"app": "firewall",
	})

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
			Labels:         map[string]string{},
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, want, nses[0].NetworkServiceLabels)
		}),
	)

	_, err := server.Request(context.Background(), request)
	require.NoError(t, err)
}

func TestMatchNonEmptySourceSelector(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()

	nsServer, nseServer := testServers(t, nsName, endpoints(), fromFirewallMatch(), fromSomeMiddleAppMatch(), fromAnywhereMatch())

	want := labels(nsName, map[string]string{
		"app": "some-middle-app",
	})

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
			Labels: map[string]string{
				"app": "firewall",
			},
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, want, nses[0].NetworkServiceLabels)
		}),
	)

	_, err := server.Request(context.Background(), request)
	require.Nil(t, err)
}

func TestMatchEmptySourceSelectorGoingFirst(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()

	nsServer, nseServer := testServers(t, nsName, endpoints(), fromAnywhereMatch(), fromFirewallMatch(), fromSomeMiddleAppMatch())

	want := labels(nsName, map[string]string{
		"app": "firewall",
	})

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
			Labels: map[string]string{
				"app": "firewall",
			},
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, want, nses[0].NetworkServiceLabels)
		}),
	)

	_, err := server.Request(context.Background(), request)
	require.NoError(t, err)
}

func TestMatchNothing(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()

	nsServer, nseServer := testServers(t, nsName, endpoints(), fromFirewallMatch(), fromSomeMiddleAppMatch())

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: "secure-intranet-connectivity",
			Labels: map[string]string{
				"app": "unknown-app",
			},
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 3)
		}),
	)

	_, err := server.Request(context.Background(), request)
	require.NoError(t, err)
}

func TestMatchSelectedNSE(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()
	nses := endpoints()

	nsServer, nseServer := testServers(t, nsName, nses, fromAnywhereMatch(), fromFirewallMatch(), fromSomeMiddleAppMatch())

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkServiceEndpointName: nses[0].Name,
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			require.NotNil(t, clienturlctx.ClientURL(ctx))
		}),
	)

	_, err := server.Request(context.Background(), request)
	require.NoError(t, err)
}

func TestNoMatchServiceFound(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()

	nsServer, nseServer := testServers(t, "", endpoints())

	want := labels(nsName, map[string]string{
		"app": "firewall",
	})

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
			Payload:        payload.IP,
			Labels:         map[string]string{},
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, want, nses[0].NetworkServiceLabels)
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second/2)
	defer cancel()

	_, err := server.Request(ctx, request)
	require.Error(t, err)
}

func TestNoMatchServiceEndpointFound(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsName := networkServiceName()

	nsServer, nseServer := testServers(t, nsName, []*registry.NetworkServiceEndpoint{}, fromFirewallMatch(), fromSomeMiddleAppMatch(), fromAnywhereMatch())

	want := labels(nsName, map[string]string{
		"app": "firewall",
	})

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
			Labels:         map[string]string{},
		},
	}

	server := next.NewNetworkServiceServer(
		discover.NewServer(adapters.NetworkServiceServerToClient(nsServer), adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, want, nses[0].NetworkServiceLabels)
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second/2)
	defer cancel()

	_, err := server.Request(ctx, request)
	require.Error(t, err)
}

func TestMatchExactService(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsServer, nseServer := testServers(t, "", []*registry.NetworkServiceEndpoint{})

	nsName := networkServiceName()
	server := next.NewNetworkServiceServer(
		discover.NewServer(
			adapters.NetworkServiceServerToClient(nsServer),
			adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, nsName, nses[0].NetworkServiceNames[0])
		}),
	)

	// 1. Register NS, NSE with wrong name
	wrongNSName := nsName + "-wrong"
	_, err := nsServer.Register(context.Background(), &registry.NetworkService{
		Name: wrongNSName,
	})
	require.NoError(t, err)
	_, err = nseServer.Register(context.Background(), &registry.NetworkServiceEndpoint{
		NetworkServiceNames: []string{wrongNSName},
	})
	require.NoError(t, err)

	// 2. Try to discover NSE by the right NS name
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
		},
	}

	_, err = server.Request(ctx, request.Clone())
	require.Error(t, err)

	// 3. Register NS, NSE with the right name
	_, err = nsServer.Register(context.Background(), &registry.NetworkService{
		Name:    nsName,
		Payload: payload.IP,
	})
	require.NoError(t, err)
	_, err = nseServer.Register(context.Background(), &registry.NetworkServiceEndpoint{
		NetworkServiceNames: []string{nsName},
	})
	require.NoError(t, err)

	// 4. Try to discover NSE by the right NS name
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	conn, err := server.Request(ctx, request.Clone())
	require.NoError(t, err)
	require.Equal(t, payload.IP, conn.Payload)
}

func TestMatchExactEndpoint(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nseServer := memory.NewNetworkServiceEndpointRegistryServer()

	nseName := "final-endpoint"
	u := "tcp://" + nseName
	server := next.NewNetworkServiceServer(
		discover.NewServer(
			adapters.NetworkServiceServerToClient(memory.NewNetworkServiceRegistryServer()),
			adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			require.Equal(t, u, clienturlctx.ClientURL(ctx).String())
		}),
	)

	// 1. Register NSE with wrong name
	wrongNSEName := nseName + "-wrong"
	wrongURL := u + "-wrong"
	_, err := nseServer.Register(context.Background(), &registry.NetworkServiceEndpoint{
		Name: wrongNSEName,
		Url:  wrongURL,
	})
	require.NoError(t, err)

	// 2. Try to discover NSE by the right name
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkServiceEndpointName: nseName,
		},
	}

	_, err = server.Request(ctx, request.Clone())
	require.Error(t, err)

	// 3. Register NSE with the right name
	_, err = nseServer.Register(context.Background(), &registry.NetworkServiceEndpoint{
		Name: nseName,
		Url:  u,
	})
	require.NoError(t, err)

	// 4. Try to discover NSE by the right name
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err = server.Request(ctx, request.Clone())
	require.NoError(t, err)
}

func TestMatchSelectedNSESecondAttempt(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsServer := memory.NewNetworkServiceRegistryServer()
	nseServer := registrynext.NewNetworkServiceEndpointRegistryServer(
		setid.NewNetworkServiceEndpointRegistryServer(),
		memory.NewNetworkServiceEndpointRegistryServer(),
	)

	nsName := networkServiceName()
	counter := 0
	server := next.NewNetworkServiceServer(
		discover.NewServer(
			adapters.NetworkServiceServerToClient(nsServer),
			adapters.NetworkServiceEndpointServerToClient(nseServer)),
		checkcontext.NewServer(t, func(t *testing.T, ctx context.Context) {
			nses := discover.Candidates(ctx).Endpoints
			require.Len(t, nses, 1)
			require.Equal(t, nsName, nses[0].NetworkServiceNames[0])
		}),
		&injectConditionServer{
			condition: func() bool {
				counter++
				return counter > 1
			},
		},
	)

	_, err := nsServer.Register(context.Background(), &registry.NetworkService{
		Name: nsName,
	})
	require.NoError(t, err)
	_, err = nseServer.Register(context.Background(), &registry.NetworkServiceEndpoint{
		NetworkServiceNames: []string{nsName},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	request := &networkservice.NetworkServiceRequest{
		Connection: &networkservice.Connection{
			NetworkService: nsName,
		},
	}
	now := time.Now()
	_, err = server.Request(ctx, request)
	require.NoError(t, err)
	require.Condition(t, func() (success bool) {
		return time.Now().After(now.Add(time.Second / 10))
	})
}

type injectConditionServer struct {
	condition func() bool
}

func (c *injectConditionServer) Request(ctx context.Context, request *networkservice.NetworkServiceRequest) (*networkservice.Connection, error) {
	if !c.condition() {
		return nil, errors.New("error originated by failed condition in injectConditionServer")
	}
	return next.Server(ctx).Request(ctx, request)
}

func (c *injectConditionServer) Close(ctx context.Context, connection *networkservice.Connection) (*empty.Empty, error) {
	if !c.condition() {
		return nil, errors.New("error originated by failed condition in injectConditionServer")
	}
	return next.Server(ctx).Close(ctx, connection)
}
