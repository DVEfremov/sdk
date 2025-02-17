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

// Package nsmgr_test define a tests for NSMGR chain element.
package nsmgr_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/networkservicemesh/api/pkg/api/networkservice"
	"github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/cls"
	kernelmech "github.com/networkservicemesh/api/pkg/api/networkservice/mechanisms/kernel"
	"github.com/networkservicemesh/api/pkg/api/registry"

	"github.com/networkservicemesh/sdk/pkg/tools/sandbox"
)

const (
	tick    = 10 * time.Millisecond
	timeout = 10 * time.Second
)

func TestNSMGR_HealEndpoint(t *testing.T) {
	testNSMGRHealEndpoint(t, false)
}

func TestNSMGR_HealEndpointRestored(t *testing.T) {
	testNSMGRHealEndpoint(t, true)
}

func testNSMGRHealEndpoint(t *testing.T, restored bool) {
	t.Cleanup(func() { goleak.VerifyNone(t) })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	defer cancel()
	domain := sandbox.NewBuilder(t).
		SetNodesCount(2).
		SetRegistryProxySupplier(nil).
		SetContext(ctx).
		Build()

	expireTime := timestamppb.New(time.Now().Add(time.Second))
	nseReg := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint",
		NetworkServiceNames: []string{"my-service-remote"},
		ExpirationTime:      expireTime,
	}

	counter := &counterServer{}
	nseCtx, nseCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer nseCtxCancel()

	nse, err := domain.Nodes[0].NewEndpoint(nseCtx, nseReg, sandbox.GenerateExpiringToken(time.Second), counter)
	require.NoError(t, err)

	request := &networkservice.NetworkServiceRequest{
		MechanismPreferences: []*networkservice.Mechanism{
			{Cls: cls.LOCAL, Type: kernelmech.MECHANISM},
		},
		Connection: &networkservice.Connection{
			Id:             "1",
			NetworkService: "my-service-remote",
			Context:        &networkservice.ConnectionContext{},
		},
	}

	nsc := domain.Nodes[1].NewClient(ctx, sandbox.GenerateTestToken)

	conn, err := nsc.Request(ctx, request.Clone())
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 1, counter.UniqueRequests())
	require.Equal(t, 8, len(conn.Path.PathSegments))

	nseCtxCancel()

	// Wait grpc unblock the port
	require.Eventually(t, checkURLFree(nse.URL.Host), timeout, tick)

	nseReg2 := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint2",
		NetworkServiceNames: []string{"my-service-remote"},
	}
	if restored {
		nseReg2.Url = nse.URL.String()
	}
	_, err = domain.Nodes[0].NewEndpoint(ctx, nseReg2, sandbox.GenerateTestToken, counter)
	require.NoError(t, err)

	// Wait NSE expired and reconnecting to the new NSE
	require.Eventually(t, checkSecondRequestsReceived(counter.UniqueRequests), timeout, tick)
	require.Equal(t, 2, counter.UniqueRequests())

	// Check refresh
	request.Connection = conn
	_, err = nsc.Request(ctx, request.Clone())
	require.NoError(t, err)

	// Close.
	e, err := nsc.Close(ctx, conn)
	require.NoError(t, err)
	require.NotNil(t, e)
	require.Equal(t, 2, counter.UniqueRequests())
	require.Equal(t, 1, counter.UniqueCloses())
}

func TestNSMGR_HealLocalForwarder(t *testing.T) {
	forwarderCtx, forwarderCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer forwarderCtxCancel()

	customConfig := []*sandbox.NodeConfig{
		nil,
		{
			ForwarderCtx:               forwarderCtx,
			ForwarderGenerateTokenFunc: sandbox.GenerateExpiringToken(time.Second),
		},
	}

	testNSMGRHealForwarder(t, 1, false, customConfig, forwarderCtxCancel)
}

func TestNSMGR_HealLocalForwarderRestored(t *testing.T) {
	forwarderCtx, forwarderCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer forwarderCtxCancel()

	customConfig := []*sandbox.NodeConfig{
		nil,
		{
			ForwarderCtx:               forwarderCtx,
			ForwarderGenerateTokenFunc: sandbox.GenerateExpiringToken(time.Second),
		},
	}

	testNSMGRHealForwarder(t, 1, true, customConfig, forwarderCtxCancel)
}

func TestNSMGR_HealRemoteForwarder(t *testing.T) {
	forwarderCtx, forwarderCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer forwarderCtxCancel()

	customConfig := []*sandbox.NodeConfig{
		{
			ForwarderCtx:               forwarderCtx,
			ForwarderGenerateTokenFunc: sandbox.GenerateExpiringToken(time.Second),
		},
	}

	testNSMGRHealForwarder(t, 0, false, customConfig, forwarderCtxCancel)
}

func TestNSMGR_HealRemoteForwarderRestored(t *testing.T) {
	forwarderCtx, forwarderCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer forwarderCtxCancel()

	customConfig := []*sandbox.NodeConfig{
		{
			ForwarderCtx:               forwarderCtx,
			ForwarderGenerateTokenFunc: sandbox.GenerateExpiringToken(time.Second),
		},
	}

	testNSMGRHealForwarder(t, 0, true, customConfig, forwarderCtxCancel)
}

func testNSMGRHealForwarder(t *testing.T, nodeNum int, restored bool, customConfig []*sandbox.NodeConfig, forwarderCtxCancel context.CancelFunc) {
	t.Cleanup(func() { goleak.VerifyNone(t) })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	builder := sandbox.NewBuilder(t)
	domain := builder.
		SetNodesCount(2).
		SetRegistryProxySupplier(nil).
		SetContext(ctx).
		SetCustomConfig(customConfig).
		Build()

	nseReg := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint",
		NetworkServiceNames: []string{"my-service-remote"},
	}

	counter := &counterServer{}
	_, err := domain.Nodes[0].NewEndpoint(ctx, nseReg, sandbox.GenerateTestToken, counter)
	require.NoError(t, err)

	request := &networkservice.NetworkServiceRequest{
		MechanismPreferences: []*networkservice.Mechanism{
			{Cls: cls.LOCAL, Type: kernelmech.MECHANISM},
		},
		Connection: &networkservice.Connection{
			Id:             "1",
			NetworkService: "my-service-remote",
			Context:        &networkservice.ConnectionContext{},
		},
	}

	nsc := domain.Nodes[1].NewClient(ctx, sandbox.GenerateTestToken)

	conn, err := nsc.Request(ctx, request.Clone())
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 1, counter.UniqueRequests())
	require.Equal(t, 8, len(conn.Path.PathSegments))

	forwarderCtxCancel()

	// Wait grpc unblock the port
	require.GreaterOrEqual(t, 1, len(domain.Nodes[nodeNum].Forwarder))
	require.Eventually(t, checkURLFree(domain.Nodes[nodeNum].Forwarder[0].URL.Host), timeout, tick)

	nseReg.Name = "cross-nse-restored"
	forwarderReg := &registry.NetworkServiceEndpoint{
		Name: "forwarder-restored",
	}
	if restored {
		forwarderReg.Url = domain.Nodes[nodeNum].Forwarder[0].URL.String()
	}
	_, err = domain.Nodes[nodeNum].NewForwarder(ctx, forwarderReg, sandbox.GenerateTestToken)
	require.NoError(t, err)

	// Wait Cross NSE expired and reconnecting through the new Cross NSE
	if restored {
		require.Eventually(t, checkSecondRequestsReceived(func() int {
			return int(atomic.LoadInt32(&counter.Requests))
		}), timeout, tick)
	} else {
		require.Eventually(t, checkSecondRequestsReceived(counter.UniqueRequests), timeout, tick)
	}
	if restored {
		require.Equal(t, int32(2), atomic.LoadInt32(&counter.Requests))
	} else {
		require.Equal(t, 2, counter.UniqueRequests())
	}

	// Check refresh
	request.Connection = conn
	_, err = nsc.Request(ctx, request.Clone())
	require.NoError(t, err)

	// Close.
	closes := atomic.LoadInt32(&counter.Closes)
	e, err := nsc.Close(ctx, conn)
	require.NoError(t, err)
	require.NotNil(t, e)

	if restored {
		require.Equal(t, int32(3), atomic.LoadInt32(&counter.Requests))
		require.Equal(t, closes+1, atomic.LoadInt32(&counter.Closes))
	} else {
		require.Equal(t, 2, counter.UniqueRequests())
		require.Equal(t, 2, counter.UniqueCloses())
	}
}

func TestNSMGR_HealRemoteNSMgrRestored(t *testing.T) {
	t.Skip("https://github.com/networkservicemesh/sdk/issues/770")

	nsmgrCtx, nsmgrCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer nsmgrCtxCancel()

	customConfig := []*sandbox.NodeConfig{
		{
			NsmgrCtx:                   nsmgrCtx,
			NsmgrGenerateTokenFunc:     sandbox.GenerateExpiringToken(time.Second),
			ForwarderCtx:               nsmgrCtx,
			ForwarderGenerateTokenFunc: sandbox.GenerateExpiringToken(time.Second),
		},
	}

	testNSMGRHealNSMgr(t, 0, customConfig, nsmgrCtxCancel)
}

func testNSMGRHealNSMgr(t *testing.T, nodeNum int, customConfig []*sandbox.NodeConfig, nsmgrCtxCancel context.CancelFunc) {
	t.Cleanup(func() { goleak.VerifyNone(t) })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	builder := sandbox.NewBuilder(t)
	domain := builder.
		SetNodesCount(2).
		SetRegistryProxySupplier(nil).
		SetContext(ctx).
		SetCustomConfig(customConfig).
		Build()

	nseReg := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint",
		NetworkServiceNames: []string{"my-service-remote"},
	}

	counter := &counterServer{}
	nse, err := domain.Nodes[0].NewEndpoint(ctx, nseReg, sandbox.GenerateTestToken, counter)
	require.NoError(t, err)

	request := &networkservice.NetworkServiceRequest{
		MechanismPreferences: []*networkservice.Mechanism{
			{Cls: cls.LOCAL, Type: kernelmech.MECHANISM},
		},
		Connection: &networkservice.Connection{
			Id:             "1",
			NetworkService: "my-service-remote",
			Context:        &networkservice.ConnectionContext{},
		},
	}

	nsc := domain.Nodes[1].NewClient(ctx, sandbox.GenerateTestToken)

	conn, err := nsc.Request(ctx, request.Clone())
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 1, counter.UniqueRequests())
	require.Equal(t, 8, len(conn.Path.PathSegments))

	nsmgrCtxCancel()
	require.Equal(t, int32(1), atomic.LoadInt32(&counter.Requests))
	require.Equal(t, int32(0), atomic.LoadInt32(&counter.Closes))

	require.Eventually(t, checkURLFree(domain.Nodes[nodeNum].NSMgr.URL.Host), timeout, tick)

	restoredNSMgrEntry, restoredNSMgrResources := builder.NewNSMgr(ctx, domain.Nodes[nodeNum], domain.Nodes[nodeNum].NSMgr.URL.Host, domain.Registry.URL, sandbox.GenerateTestToken)
	domain.Nodes[nodeNum].NSMgr = restoredNSMgrEntry
	domain.AddResources(restoredNSMgrResources)

	forwarderReg := &registry.NetworkServiceEndpoint{
		Name: "forwarder-restored",
	}
	_, err = domain.Nodes[nodeNum].NewForwarder(ctx, forwarderReg, sandbox.GenerateTestToken)
	require.NoError(t, err)

	nseReg.Url = nse.URL.String()
	err = domain.Nodes[nodeNum].RegisterEndpoint(ctx, nseReg)
	require.NoError(t, err)

	// Wait Cross NSE expired and reconnecting through the new Cross NSE
	require.Eventually(t, checkSecondRequestsReceived(func() int {
		return int(atomic.LoadInt32(&counter.Requests))
	}), timeout, tick)

	// Check refresh
	request.Connection = conn
	_, err = nsc.Request(ctx, request.Clone())
	require.NoError(t, err)

	// Close.
	closes := atomic.LoadInt32(&counter.Closes)
	e, err := nsc.Close(ctx, conn)
	require.NoError(t, err)
	require.NotNil(t, e)
	require.Equal(t, int32(2), atomic.LoadInt32(&counter.Requests))
	require.Equal(t, closes+1, atomic.LoadInt32(&counter.Closes))
}

func TestNSMGR_HealRemoteNSMgr(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	nsmgrCtx, nsmgrCtxCancel := context.WithTimeout(context.Background(), time.Second)
	defer nsmgrCtxCancel()
	customConfig := []*sandbox.NodeConfig{
		{
			NsmgrCtx:                   nsmgrCtx,
			NsmgrGenerateTokenFunc:     sandbox.GenerateExpiringToken(time.Second),
			ForwarderCtx:               nsmgrCtx,
			ForwarderGenerateTokenFunc: sandbox.GenerateExpiringToken(time.Second),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	builder := sandbox.NewBuilder(t)
	domain := builder.
		SetNodesCount(3).
		SetRegistryProxySupplier(nil).
		SetContext(ctx).
		SetCustomConfig(customConfig).
		Build()

	nseReg := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint",
		NetworkServiceNames: []string{"my-service-remote"},
	}

	counter := &counterServer{}
	_, err := domain.Nodes[0].NewEndpoint(ctx, nseReg, sandbox.GenerateTestToken, counter)
	require.NoError(t, err)

	request := &networkservice.NetworkServiceRequest{
		MechanismPreferences: []*networkservice.Mechanism{
			{Cls: cls.LOCAL, Type: kernelmech.MECHANISM},
		},
		Connection: &networkservice.Connection{
			Id:             "1",
			NetworkService: "my-service-remote",
			Context:        &networkservice.ConnectionContext{},
		},
	}

	nsc := domain.Nodes[1].NewClient(ctx, sandbox.GenerateTestToken)

	conn, err := nsc.Request(ctx, request.Clone())
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 1, counter.UniqueRequests())
	require.Equal(t, 8, len(conn.Path.PathSegments))

	nsmgrCtxCancel()

	nseReg2 := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint-2",
		NetworkServiceNames: []string{"my-service-remote"},
	}
	_, err = domain.Nodes[2].NewEndpoint(ctx, nseReg2, sandbox.GenerateTestToken, counter)
	require.NoError(t, err)

	// Wait Cross NSE expired and reconnecting through the new Cross NSE
	require.Eventually(t, checkSecondRequestsReceived(counter.UniqueRequests), timeout, tick)
	require.Equal(t, 2, counter.UniqueRequests())

	// Check refresh
	request.Connection = conn
	_, err = nsc.Request(ctx, request.Clone())
	require.NoError(t, err)

	// Close.
	e, err := nsc.Close(ctx, conn)
	require.NoError(t, err)
	require.NotNil(t, e)
	require.Equal(t, 2, counter.UniqueRequests())
	require.Equal(t, 2, counter.UniqueCloses())
}

func TestNSMGR_CloseHeal(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t) })

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	domain := sandbox.NewBuilder(t).
		SetNodesCount(1).
		SetRegistryProxySupplier(nil).
		SetContext(ctx).
		Build()

	nseReg := &registry.NetworkServiceEndpoint{
		Name:                "final-endpoint",
		NetworkServiceNames: []string{"my-service"},
	}

	nseCtx, nseCtxCancel := context.WithCancel(ctx)

	_, err := domain.Nodes[0].NewEndpoint(nseCtx, nseReg, sandbox.GenerateTestToken)
	require.NoError(t, err)

	request := &networkservice.NetworkServiceRequest{
		MechanismPreferences: []*networkservice.Mechanism{
			{Cls: cls.LOCAL, Type: kernelmech.MECHANISM},
		},
		Connection: &networkservice.Connection{
			Id:             "1",
			NetworkService: "my-service",
			Context:        &networkservice.ConnectionContext{},
		},
	}

	nscCtx, nscCtxCancel := context.WithCancel(ctx)

	nsc := domain.Nodes[0].NewClient(nscCtx, sandbox.GenerateTestToken)

	// 1. Request
	conn, err := nsc.Request(ctx, request.Clone())
	require.NoError(t, err)

	ignoreCurrent := goleak.IgnoreCurrent()

	// 2. Refresh
	request.Connection = conn

	conn, err = nsc.Request(ctx, request.Clone())
	require.NoError(t, err)

	// 3. Stop endpoint
	nseCtxCancel()

	// 4. Close connection
	_, _ = nsc.Close(nscCtx, conn.Clone())

	nscCtxCancel()

	require.Eventually(t, func() bool {
		return goleak.Find(ignoreCurrent) == nil
	}, timeout, tick)

	require.NoError(t, ctx.Err())
}

func checkSecondRequestsReceived(requestsDone func() int) func() bool {
	return func() bool {
		return requestsDone() == 2
	}
}

func checkURLFree(url string) func() bool {
	return func() bool {
		ln, err := net.Listen("tcp", url)
		if err != nil {
			return false
		}
		err = ln.Close()
		return err == nil
	}
}
