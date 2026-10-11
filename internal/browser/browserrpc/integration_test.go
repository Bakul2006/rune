// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package browserrpc

import (
	context "context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"go.uber.org/goleak"
	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/workspace/processctx"
)

// integrationExtensionID tags every call as extensionv2's interceptor
// does for an authenticated extension.
const integrationExtensionID = "test-extension"

func newClientServerIntegration(
	t *testing.T, h browser.Browser,
) (*browserrpc.Client, func()) {
	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	mutex := new(sync.Mutex)

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(
		ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		return handler(processctx.ContextWithExtensionID(ctx, integrationExtensionID), req)
	}))
	rpcServer := NewServer(h, mutex)
	rpcServer.SetSyncMode()
	browserrpc.RegisterWindowManagerServer(grpcServer, rpcServer)
	browserrpc.RegisterResourceOpenerServer(grpcServer, rpcServer)
	browserrpc.RegisterNotificationsServer(grpcServer, rpcServer)
	browserrpc.RegisterEventPublisherServer(grpcServer, rpcServer)

	go grpcServer.Serve(lis)

	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithInsecure())
	require.NoError(t, err)

	client := browserrpc.NewClient(context.Background(), conn)

	closeFn := func() {
		client.Close()
		rpcServer.browser.Lock()
		rpcServer.Stop()
		rpcServer.browser.Unlock()
		grpcServer.Stop()
		conn.Close()
	}

	return client, closeFn
}

func TestClientServerIntegrationSplit(t *testing.T) {
	tsuite := []browserapi.Orientation{
		browserapi.OrientationDefault,
		browserapi.OrientationTop,
		browserapi.OrientationBottom,
		browserapi.OrientationLeft,
		browserapi.OrientationRight,
	}

	for _, _tcase := range tsuite {
		tcase := _tcase
		t.Run(fmt.Sprintf("%+v", tcase), func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := browsertest.NewMockBrowser(ctrl)
			client, cleanup := newClientServerIntegration(t, mock)
			defer cleanup()

			win0 := browsertest.NopWindow()
			win1 := browsertest.NopWindow()

			mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
			mock.EXPECT().Split(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(
					o browserapi.Orientation, win browserapi.Window, h browserapi.Handler,
				) (browser.Window, error) {
					assert.Equal(t, tcase, o)
					return win1, nil
				})
			resWin1, err := client.Split(tcase, win0, browsertest.NewTestHandler())
			require.NoError(t, err)

			mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
			require.NoError(t, client.SetWindowContent(resWin1, browsertest.NewTestHandler()))
			require.NoError(t, client.CloseWindow(resWin1))
		})
	}
}

func TestClientServerIntegrationBar(t *testing.T) {
	tsuite := []browserapi.BarConfig{
		{Orientation: browserapi.OrientationDefault, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationTop, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationBottom, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationLeft, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationRight, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationDefault, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationTop, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationBottom, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationLeft, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationRight, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationDefault, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationTop, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationBottom, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationLeft, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationRight, Frame: browserapi.BarFrameNever, Size: 1},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase
		t.Run(fmt.Sprintf("%+v", tcase), func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := browsertest.NewMockBrowser(ctrl)
			client, cleanup := newClientServerIntegration(t, mock)
			defer cleanup()

			win1 := browsertest.NopWindow()

			mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
			mock.EXPECT().Bar(gomock.Any(), gomock.Any()).
				DoAndReturn(func(
					config browserapi.BarConfig, h tui.Handler,
				) error {
					assert.Equal(t, tcase.Orientation, config.Orientation)
					assert.Equal(t, tcase.Frame, config.Frame)
					assert.Equal(t, tcase.Size, config.Size)
					return nil
				})
			err := client.Bar(tcase, browsertest.NewTestHandler())
			require.NoError(t, err)
		})
	}
}

func TestClientServerIntegrationTab(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := browsertest.NewMockBrowser(ctrl)
	client, cleanup := newClientServerIntegration(t, mock)
	defer cleanup()

	expectedURI, err := workspaceapi.ParseURI("ABV://SanCarlos@2017/BlueLime")
	require.NoError(t, err)
	expectedIcon := 'X'
	expectedName := "Linduro"

	mock.EXPECT().Resource(gomock.Any()).Return(browsertest.NewTestHandler(), true)
	mock.EXPECT().Tab(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
		) (browserapi.Handler, error) {
			assert.Equal(t, expectedURI, uri)
			assert.Equal(t, expectedIcon, icon)
			assert.Equal(t, expectedName, name)
			return browsertest.NewTestHandler(), nil
		})
	tab, err := client.Tab(expectedURI, expectedIcon,
		expectedName, browsertest.NewTestHandler())
	require.NoError(t, err)

	win1 := browsertest.NopWindow()
	mock.EXPECT().Focus().Return(win1, nil)

	win, err := client.Focus()
	require.NoError(t, err)

	mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
	require.NoError(t, client.SetWindowContent(win, tab))
}

func TestClientServerIntegrationSetTabStatus(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := browsertest.NewMockBrowser(ctrl)
	client, cleanup := newClientServerIntegration(t, mock)
	defer cleanup()

	uri, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)

	blocked := browserapi.TabStatus{
		ID: "tool/bash", State: browserapi.TabBlocked, Kind: browserapi.TabKindPermission,
		Progress: 40, Title: "make test", Message: "Approve apply?",
	}
	wantBlocked := programstatus.Report{
		ID: "tool/bash", State: programstatus.Blocked, Kind: programstatus.KindPermission,
		Progress: 40, App: integrationExtensionID, Title: "make test", Msg: "Approve apply?",
	}
	clear := browserapi.TabStatus{State: browserapi.TabClear, Progress: -1}
	gomock.InOrder(
		mock.EXPECT().SetTabStatus(uri, wantBlocked).Return(nil),
		mock.EXPECT().SetTabStatus(uri, programstatus.Report{
			State: programstatus.Clear, Progress: -1,
		}).Return(errors.New("unknown tab")),
	)
	require.NoError(t, client.SetTabStatus(uri, blocked))
	err = client.SetTabStatus(uri, clear)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tab")

	err = client.SetTabStatus(uri, browserapi.TabStatus{State: 99})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tab state")

	err = client.SetTabStatus(uri, browserapi.TabStatus{State: browserapi.TabWorking, ID: "a//b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid id")
}
