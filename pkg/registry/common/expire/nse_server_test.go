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

package expire_test

import (
	"context"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/networkservicemesh/api/pkg/api/registry"

	"github.com/networkservicemesh/sdk/pkg/registry/common/expire"
	"github.com/networkservicemesh/sdk/pkg/registry/common/localbypass"
	"github.com/networkservicemesh/sdk/pkg/registry/common/memory"
	"github.com/networkservicemesh/sdk/pkg/registry/common/refresh"
	"github.com/networkservicemesh/sdk/pkg/registry/core/adapters"
	"github.com/networkservicemesh/sdk/pkg/registry/core/next"
	"github.com/networkservicemesh/sdk/pkg/registry/utils/checks/checkcontext"
)

func TestExpireNSEServer_ShouldCorrectlySetExpirationTime_InRemoteCase(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	s := next.NewNetworkServiceEndpointRegistryServer(
		expire.NewNetworkServiceEndpointRegistryServer(context.Background(), time.Hour),
		new(remoteNSEServer),
	)

	resp, err := s.Register(context.Background(), &registry.NetworkServiceEndpoint{Name: "nse-1"})
	require.NoError(t, err)

	require.Greater(t, time.Until(resp.ExpirationTime.AsTime()).Minutes(), float64(50))
}

func TestExpireNSEServer_ShouldUseLessExpirationTimeFromInput_AndWork(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	s := next.NewNetworkServiceEndpointRegistryServer(
		expire.NewNetworkServiceEndpointRegistryServer(context.Background(), time.Hour),
		memory.NewNetworkServiceEndpointRegistryServer(),
	)

	resp, err := s.Register(context.Background(), &registry.NetworkServiceEndpoint{
		Name:           "nse-1",
		ExpirationTime: timestamppb.New(time.Now().Add(time.Millisecond * 200)),
	})
	require.NoError(t, err)

	require.Less(t, time.Until(resp.ExpirationTime.AsTime()).Seconds(), float64(65))

	c := adapters.NetworkServiceEndpointServerToClient(s)

	require.Eventually(t, func() bool {
		stream, err := c.Find(context.Background(), &registry.NetworkServiceEndpointQuery{
			NetworkServiceEndpoint: new(registry.NetworkServiceEndpoint),
		})
		require.NoError(t, err)

		list := registry.ReadNetworkServiceEndpointList(stream)
		return len(list) == 0
	}, time.Second, time.Millisecond*100)
}

func TestExpireNSEServer_ShouldUseLessExpirationTimeFromResponse(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	s := next.NewNetworkServiceEndpointRegistryServer(
		expire.NewNetworkServiceEndpointRegistryServer(context.Background(), time.Hour),
		new(remoteNSEServer), // <-- GRPC invocation
		expire.NewNetworkServiceEndpointRegistryServer(context.Background(), 10*time.Minute),
	)

	resp, err := s.Register(context.Background(), &registry.NetworkServiceEndpoint{Name: "nse-1"})
	require.NoError(t, err)

	require.Less(t, time.Until(resp.ExpirationTime.AsTime()).Minutes(), float64(11))
}

func TestExpireNSEServer_ShouldRemoveNSEAfterExpirationTime(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	s := next.NewNetworkServiceEndpointRegistryServer(
		expire.NewNetworkServiceEndpointRegistryServer(context.Background(), testPeriod*2),
		new(remoteNSEServer), // <-- GRPC invocation
		memory.NewNetworkServiceEndpointRegistryServer(),
	)

	_, err := s.Register(context.Background(), &registry.NetworkServiceEndpoint{})
	require.NoError(t, err)

	c := adapters.NetworkServiceEndpointServerToClient(s)
	stream, err := c.Find(context.Background(), &registry.NetworkServiceEndpointQuery{
		NetworkServiceEndpoint: new(registry.NetworkServiceEndpoint),
	})
	require.NoError(t, err)

	list := registry.ReadNetworkServiceEndpointList(stream)
	require.NotEmpty(t, list)

	require.Eventually(t, func() bool {
		stream, err = c.Find(context.Background(), &registry.NetworkServiceEndpointQuery{
			NetworkServiceEndpoint: new(registry.NetworkServiceEndpoint),
		})
		require.NoError(t, err)

		list = registry.ReadNetworkServiceEndpointList(stream)
		return len(list) == 0
	}, time.Second, time.Millisecond*100)
}

func TestExpireNSEServer_DataRace(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	mem := memory.NewNetworkServiceEndpointRegistryServer()

	s := next.NewNetworkServiceEndpointRegistryServer(
		expire.NewNetworkServiceEndpointRegistryServer(context.Background(), 0),
		localbypass.NewNetworkServiceEndpointRegistryServer("tcp://0.0.0.0"),
		mem,
	)

	for i := 0; i < 1000; i++ {
		_, err := s.Register(context.Background(), &registry.NetworkServiceEndpoint{
			Name: "nse-1",
			Url:  "tcp://1.1.1.1",
		})
		require.NoError(t, err)
	}

	c := adapters.NetworkServiceEndpointServerToClient(mem)

	require.Eventually(t, func() bool {
		stream, err := c.Find(context.Background(), &registry.NetworkServiceEndpointQuery{
			NetworkServiceEndpoint: new(registry.NetworkServiceEndpoint),
		})
		require.NoError(t, err)

		list := registry.ReadNetworkServiceEndpointList(stream)
		return len(list) == 0
	}, time.Second, time.Millisecond*100)
}

func TestExpireNSEServer_RefreshFailure(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := next.NewNetworkServiceEndpointRegistryClient(
		refresh.NewNetworkServiceEndpointRegistryClient(refresh.WithChainContext(ctx)),
		adapters.NetworkServiceEndpointServerToClient(next.NewNetworkServiceEndpointRegistryServer(
			new(remoteNSEServer), // <-- GRPC invocation
			expire.NewNetworkServiceEndpointRegistryServer(ctx, testPeriod),
			newFailureNSEServer(1, -1),
			memory.NewNetworkServiceEndpointRegistryServer(),
		)),
	)

	_, err := c.Register(ctx, &registry.NetworkServiceEndpoint{Name: "nse-1"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		stream, err := c.Find(context.Background(), &registry.NetworkServiceEndpointQuery{
			NetworkServiceEndpoint: new(registry.NetworkServiceEndpoint),
		})
		require.NoError(t, err)

		list := registry.ReadNetworkServiceEndpointList(stream)
		return len(list) == 0
	}, time.Second, time.Millisecond*100)
}

func TestExpireNSEServer_RefreshKeepsNoUnregister(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mem := memory.NewNetworkServiceEndpointRegistryServer()

	c := next.NewNetworkServiceEndpointRegistryClient(
		refresh.NewNetworkServiceEndpointRegistryClient(refresh.WithChainContext(ctx)),
		adapters.NetworkServiceEndpointServerToClient(next.NewNetworkServiceEndpointRegistryServer(
			// NSMgr chain
			new(remoteNSEServer), // <-- GRPC invocation
			expire.NewNetworkServiceEndpointRegistryServer(ctx, 2*testPeriod),
			// Registry chain
			new(remoteNSEServer), // <-- GRPC invocation
			checkcontext.NewNSEServer(t, func(_ *testing.T, _ context.Context) {
				<-time.After(testPeriod)
			}),
			expire.NewNetworkServiceEndpointRegistryServer(ctx, time.Minute),
			mem,
		)),
	)

	_, err := c.Register(ctx, &registry.NetworkServiceEndpoint{Name: "nse-1"})
	require.NoError(t, err)

	stream, err := adapters.NetworkServiceEndpointServerToClient(mem).Find(ctx, &registry.NetworkServiceEndpointQuery{
		NetworkServiceEndpoint: new(registry.NetworkServiceEndpoint),
		Watch:                  true,
	})
	require.NoError(t, err)

	for start := time.Now(); time.Since(start).Seconds() < 1; {
		nse, err := stream.Recv()
		require.NoError(t, err)
		require.NotEqual(t, int64(-1), nse.ExpirationTime.Seconds)
	}
}

type remoteNSEServer struct {
	registry.NetworkServiceEndpointRegistryServer
}

func (s *remoteNSEServer) Register(ctx context.Context, nse *registry.NetworkServiceEndpoint) (*registry.NetworkServiceEndpoint, error) {
	return next.NetworkServiceEndpointRegistryServer(ctx).Register(ctx, nse.Clone())
}

func (s *remoteNSEServer) Find(query *registry.NetworkServiceEndpointQuery, server registry.NetworkServiceEndpointRegistry_FindServer) error {
	return next.NetworkServiceEndpointRegistryServer(server.Context()).Find(query, server)
}

func (s *remoteNSEServer) Unregister(ctx context.Context, nse *registry.NetworkServiceEndpoint) (*empty.Empty, error) {
	return next.NetworkServiceEndpointRegistryServer(ctx).Unregister(ctx, nse.Clone())
}

type failureNSEServer struct {
	count        int
	failureTimes []int
}

func newFailureNSEServer(failureTimes ...int) *failureNSEServer {
	return &failureNSEServer{
		failureTimes: failureTimes,
	}
}

func (s *failureNSEServer) Register(ctx context.Context, nse *registry.NetworkServiceEndpoint) (*registry.NetworkServiceEndpoint, error) {
	defer func() { s.count++ }()
	for _, failureTime := range s.failureTimes {
		if failureTime > s.count {
			break
		}
		if failureTime == s.count || failureTime == -1 {
			return nil, errors.New("failure")
		}
	}
	return next.NetworkServiceEndpointRegistryServer(ctx).Register(ctx, nse)
}

func (s *failureNSEServer) Find(query *registry.NetworkServiceEndpointQuery, server registry.NetworkServiceEndpointRegistry_FindServer) error {
	return next.NetworkServiceEndpointRegistryServer(server.Context()).Find(query, server)
}

func (s *failureNSEServer) Unregister(ctx context.Context, nse *registry.NetworkServiceEndpoint) (*empty.Empty, error) {
	return next.NetworkServiceEndpointRegistryServer(ctx).Unregister(ctx, nse)
}
