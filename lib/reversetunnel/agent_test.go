/*
Copyright 2022 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package reversetunnel

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gravitational/teleport"
	"github.com/gravitational/teleport/lib/reversetunnel/track"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type mockSSHClient struct {
	MockSendRequest       func(name string, wantReply bool, payload []byte) (bool, []byte, error)
	MockOpenChannel       func(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error)
	MockClose             func() error
	MockReply             func(*ssh.Request, bool, []byte) error
	MockPrincipals        []string
	MockGlobalRequests    chan *ssh.Request
	MockHandleChannelOpen chan ssh.NewChannel
}

func (m *mockSSHClient) User() string { return "" }

func (m *mockSSHClient) SessionID() []byte { return nil }

func (m *mockSSHClient) ClientVersion() []byte { return nil }

func (m *mockSSHClient) ServerVersion() []byte { return nil }

func (m *mockSSHClient) RemoteAddr() net.Addr { return nil }

func (m *mockSSHClient) LocalAddr() net.Addr { return nil }

func (m *mockSSHClient) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	if m.MockSendRequest != nil {
		return m.MockSendRequest(name, wantReply, payload)
	}
	return false, nil, trace.NotImplemented("")
}

func (m *mockSSHClient) OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	if m.MockOpenChannel != nil {
		return m.MockOpenChannel(name, data)
	}
	return nil, nil, trace.NotImplemented("")
}

func (m *mockSSHClient) Close() error {
	if m.MockClose != nil {
		return m.MockClose()
	}
	return nil
}

func (m *mockSSHClient) Wait() error {
	return nil
}

func (m *mockSSHClient) Principals() []string {
	return m.MockPrincipals
}

func (m *mockSSHClient) HandleChannelOpen(channelType string) <-chan ssh.NewChannel {
	return m.MockHandleChannelOpen
}

func (m *mockSSHClient) Reply(r *ssh.Request, ok bool, payload []byte) error {
	if m.MockReply != nil {
		return m.MockReply(r, ok, payload)
	}

	return trace.NotImplemented("")
}

func (m *mockSSHClient) GlobalRequests() <-chan *ssh.Request {
	return m.MockGlobalRequests
}

type mockSSHChannel struct {
	MockSendRequest func(name string, wantReply bool, payload []byte) (bool, error)
}

func (m *mockSSHChannel) Read(data []byte) (int, error) {
	return 0, trace.NotImplemented("")
}

func (m *mockSSHChannel) Write(data []byte) (int, error) {
	return 0, trace.NotImplemented("")
}

func (m *mockSSHChannel) Close() error { return nil }

func (m *mockSSHChannel) CloseWrite() error { return nil }

func (m *mockSSHChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	if m.MockSendRequest != nil {
		return m.MockSendRequest(name, wantReply, payload)
	}

	return false, trace.NotImplemented("")
}

func (m *mockSSHChannel) Stderr() io.ReadWriter {
	return nil
}

// mockAgentInjection implements several interfaces for injecting into an agent.
type mockAgentInjection struct {
	client SSHClient
}

func (m *mockAgentInjection) transport(context.Context, ssh.Channel, <-chan *ssh.Request, ssh.Conn) *transport {
	return &transport{}
}

func (m *mockAgentInjection) DialContext(context.Context, utils.NetAddr) (SSHClient, error) {
	return m.client, nil
}

func (m *mockAgentInjection) getVersion(context.Context) (string, error) {
	return teleport.Version, nil
}

func testAgent(t *testing.T) (*agent, *mockSSHClient) {
	tracker, err := track.New(context.Background(), track.Config{
		ClusterName: "test",
	})
	require.NoError(t, err)

	addr := utils.NetAddr{Addr: "test-proxy-addr"}

	tracker.Start()
	t.Cleanup(tracker.StopAll)

	lease := <-tracker.Acquire()

	client := &mockSSHClient{
		MockPrincipals:        []string{"default"},
		MockGlobalRequests:    make(chan *ssh.Request),
		MockHandleChannelOpen: make(chan ssh.NewChannel),
	}

	inject := &mockAgentInjection{
		client: client,
	}

	agent, err := newAgent(agentConfig{
		keepAlive:     time.Millisecond * 100,
		addr:          addr,
		transporter:   inject,
		sshDialer:     inject,
		versionGetter: inject,
		tracker:       tracker,
		lease:         lease,
	})
	require.NoError(t, err, "Unexpected error during agent construction.")

	return agent, client
}

type callbackCounter struct {
	calls  int
	states []AgentState
	recv   chan struct{}
	mu     sync.Mutex
}

func newCallback() *callbackCounter {
	return &callbackCounter{
		recv: make(chan struct{}, 1),
	}
}

func (c *callbackCounter) callback(state AgentState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.states = append(c.states, state)

	select {
	case c.recv <- struct{}{}:
	default:
	}
}

func (c *callbackCounter) waitForCount(t *testing.T, count int) {
	timer := time.NewTimer(time.Second * 5)
	for {
		c.mu.Lock()
		if c.calls == count {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		select {
		case <-c.recv:
			continue
		case <-timer.C:
			require.FailNow(t, "timeout waiting for agent state changes")
		}
	}
}

// TestAgentFailedToClaimLease tests that an agent fails when it cannot claim a lease.
func TestAgentFailedToClaimLease(t *testing.T) {
	agent, client := testAgent(t)
	claimedProxy := "claimed-proxy"

	callback := newCallback()
	agent.stateCallback = callback.callback
	agent.tracker.Claim(claimedProxy)

	client.MockPrincipals = []string{claimedProxy}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err := agent.Start(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Failed to claim proxy", "Expected failed to claim proxy error.")

	callback.waitForCount(t, 2)
	require.Contains(t, callback.states, AgentConnecting)
	require.Contains(t, callback.states, AgentClosed)

	require.Equal(t, 2, callback.calls, "Unexpected number of state changes.")
	require.Equal(t, AgentClosed, agent.GetState())
}

// TestAgentStart tests an agent performs the necessary actions during startup.
func TestAgentStart(t *testing.T) {
	agent, client := testAgent(t)

	callback := newCallback()
	agent.stateCallback = callback.callback

	openChannels := new(int32)
	sentPings := new(int32)
	versionReplies := new(int32)

	waitForVersion := make(chan struct{})
	go func() {
		client.MockGlobalRequests <- &ssh.Request{
			Type: versionRequest,
		}
	}()

	client.MockOpenChannel = func(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error) {
		// Block until the version request is handled to ensure we handle
		// global requests during startup.
		<-waitForVersion

		atomic.AddInt32(openChannels, 1)
		assert.Equal(t, name, chanHeartbeat, "Unexpected channel opened during startup.")
		return &mockSSHChannel{MockSendRequest: func(name string, wantReply bool, payload []byte) (bool, error) {
			atomic.AddInt32(sentPings, 1)

			assert.Equal(t, name, "ping", "Unexpected request name.")
			assert.False(t, wantReply, "Expected no reply wanted.")
			return true, nil
		}}, make(<-chan *ssh.Request), nil
	}

	client.MockReply = func(r *ssh.Request, b1 bool, b2 []byte) error {
		atomic.AddInt32(versionReplies, 1)

		// Unblock once we receive a version reply.
		close(waitForVersion)

		assert.Equal(t, versionRequest, r.Type, "Unexpected request type.")
		assert.Equal(t, teleport.Version, string(b2), "Unexpected version.")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err := agent.Start(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, int(atomic.LoadInt32(openChannels)), "Expected only heartbeat channel to be opened.")
	require.GreaterOrEqual(t, 1, int(atomic.LoadInt32(sentPings)), "Expected at least 1 ping to be sent.")
	require.Equal(t, 1, int(atomic.LoadInt32(versionReplies)), "Expected 1 version reply.")

	callback.waitForCount(t, 2)
	require.Contains(t, callback.states, AgentConnecting)
	require.Contains(t, callback.states, AgentConnected)
	require.Equal(t, 2, callback.calls, "Unexpected number of state changes.")
	require.Equal(t, AgentConnected, agent.GetState())

	unclaimed := false
	agent.unclaim = func() {
		unclaimed = true
	}

	err = agent.Stop()
	require.NoError(t, err)
	require.True(t, unclaimed, "Expected unclaim to be called.")

	callback.waitForCount(t, 3)
	require.Contains(t, callback.states, AgentClosed)
	require.Equal(t, 3, callback.calls, "Unexpected number of state changes.")
	require.Equal(t, AgentClosed, agent.GetState())
}

func TestAgentStateTransitions(t *testing.T) {
	tests := []struct {
		start    AgentState
		noError  []AgentState
		yesError []AgentState
	}{
		{
			start:    AgentInitial,
			noError:  []AgentState{AgentConnecting, AgentClosed},
			yesError: []AgentState{AgentInitial, AgentConnected},
		},
		{
			start:    AgentConnecting,
			noError:  []AgentState{AgentConnected, AgentClosed},
			yesError: []AgentState{AgentInitial, AgentConnecting},
		},
		{
			start:    AgentConnected,
			noError:  []AgentState{AgentClosed},
			yesError: []AgentState{AgentInitial, AgentConnecting, AgentConnected},
		},
		{
			start:    AgentClosed,
			yesError: []AgentState{AgentInitial, AgentConnecting, AgentConnected, AgentClosed},
		},
	}

	agent, _ := testAgent(t)
	for _, tc := range tests {
		msg := "failed testing agent state %s -> %s"
		for _, state := range tc.noError {
			agent.state = tc.start
			prev, err := agent.updateState(state)
			require.Equal(t, tc.start, prev, msg, tc.start, state)
			require.NoError(t, err, msg, tc.start, state)
		}
		for _, state := range tc.yesError {
			agent.state = tc.start
			_, err := agent.updateState(state)
			require.Error(t, err, msg, tc.start, state)
		}
	}
}
