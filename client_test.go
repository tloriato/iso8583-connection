package client_test

import (
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/moov-io/iso8583"
	client "github.com/moovfinancial/iso8583-client"
	"github.com/moovfinancial/iso8583-client/server"
	"github.com/stretchr/testify/require"
)

func TestClient_Connect(t *testing.T) {
	t.Run("unsecure connection", func(t *testing.T) {
		server, err := NewTestServer()
		require.NoError(t, err)
		defer server.Close()

		// our client can connect to the server
		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)
		require.NoError(t, c.Close())
	})

	t.Run("with TLS", func(t *testing.T) {
		srv := http.Server{}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		go func() {
			if err := srv.ServeTLS(ln, "./testdata/server.crt", "./testdata/server.key"); err != nil {
				require.ErrorIs(t, err, http.ErrServerClosed)
			}
		}()
		defer func() {
			// let's give client the chance to close the connection first
			time.Sleep(100 * time.Millisecond)
			srv.Close()
		}()

		c, err := client.NewClient(
			ln.Addr().String(),
			testSpec,
			readMessageLength,
			writeMessageLength,
			client.ClientCert("./testdata/client.crt", "./testdata/client.key"),
			client.RootCAs("./testdata/ca.crt"),
		)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)

		require.NoError(t, c.Close())
	})

	t.Run("no panic when Close before Connect", func(t *testing.T) {
		// our client can connect to the server
		c, err := client.NewClient("", testSpec, readMessageLength, writeMessageLength)
		require.NoError(t, err)

		require.NoError(t, c.Close())
	})
}

func TestClient_Send(t *testing.T) {
	server, err := NewTestServer()
	require.NoError(t, err)
	defer server.Close()

	t.Run("sends messages to server and receives responses", func(t *testing.T) {
		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)

		// network management message
		message := iso8583.NewMessage(testSpec)
		message.MTI("0800")
		message.Field(2, CardForDelayedResponse)

		// we can send iso message to the server
		response, err := c.Send(message)
		require.NoError(t, err)
		time.Sleep(1 * time.Second)

		mti, err := response.GetMTI()
		require.NoError(t, err)
		require.Equal(t, "0810", mti)

		require.NoError(t, c.Close())
	})

	t.Run("it returns ErrConnectionClosed when Close was called", func(t *testing.T) {
		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)

		// network management message
		message := iso8583.NewMessage(testSpec)
		message.MTI("0800")
		message.Field(2, CardForDelayedResponse)

		require.NoError(t, c.Close())

		_, err = c.Send(message)
		require.Equal(t, client.ErrConnectionClosed, err)
	})

	t.Run("it returns ErrSendTimeout when response was not received during SendTimeout time", func(t *testing.T) {
		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength, client.SendTimeout(100*time.Millisecond))
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)
		defer c.Close()

		// regular network management message
		message := iso8583.NewMessage(testSpec)
		message.MTI("0800")

		_, err = c.Send(message)
		require.NoError(t, err)

		// network management message to test timeout
		message = iso8583.NewMessage(testSpec)
		message.MTI("0800")

		// using 777 value for the field, we tell server
		// to sleep for 500ms when process the message
		require.NoError(t, message.Field(2, CardForDelayedResponse))

		_, err = c.Send(message)
		require.Equal(t, client.ErrSendTimeout, err)
	})

	t.Run("pending requests should complete after Close was called", func(t *testing.T) {
		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)
		defer c.Close()

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer func() {
					wg.Done()
				}()

				// network management message
				message := iso8583.NewMessage(testSpec)
				message.MTI("0800")

				// using 777 value for the field, we tell server
				// to sleep for 500ms when process the message
				require.NoError(t, message.Field(2, CardForDelayedResponse))

				response, err := c.Send(message)
				require.NoError(t, err)

				mti, err := response.GetMTI()
				require.NoError(t, err)
				require.Equal(t, "0810", mti)
			}(i)
		}

		// let's wait all messages to be sent
		time.Sleep(200 * time.Millisecond)

		// while server is waiting, we will close the connection
		require.NoError(t, c.Close())
		wg.Wait()
	})

	t.Run("responses received asynchronously", func(t *testing.T) {
		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)
		defer c.Close()

		// send first message and tell server to respond in 500ms
		var (
			receivedSTANs []string
			wg            sync.WaitGroup
			mu            sync.Mutex
			stan1         string
			stan2         string
		)

		wg.Add(1)
		go func() {
			defer func() {
				wg.Done()
			}()

			message := iso8583.NewMessage(testSpec)
			message.MTI("0800")

			// using 777 value for the field, we tell server
			// to sleep for 500ms when process the message
			require.NoError(t, message.Field(2, CardForDelayedResponse))
			response, err := c.Send(message)
			require.NoError(t, err)

			// put received STAN into slice so we can check the order
			stan1, err = response.GetString(11)
			require.NoError(t, err)
			mu.Lock()
			receivedSTANs = append(receivedSTANs, stan1)
			mu.Unlock()
		}()

		wg.Add(1)
		go func() {
			defer func() {
				wg.Done()
			}()

			// this message will be sent after the first one
			time.Sleep(100 * time.Millisecond)

			message := iso8583.NewMessage(testSpec)
			message.MTI("0800")

			response, err := c.Send(message)
			require.NoError(t, err)

			// put received STAN into slice so we can check the order
			stan2, err = response.GetString(11)
			require.NoError(t, err)
			mu.Lock()
			receivedSTANs = append(receivedSTANs, stan2)
			mu.Unlock()
		}()

		// let's wait all messages to be sent
		time.Sleep(200 * time.Millisecond)

		wg.Wait()
		require.NoError(t, c.Close())

		// we expect that response for the second message was received first
		require.Equal(t, receivedSTANs[0], stan2)

		// and that response for the first message was received second
		require.Equal(t, receivedSTANs[1], stan1)

	})

	t.Run("automatically sends ping messages after ping interval", func(t *testing.T) {
		// we create server instance here to isolate pings count
		server, err := NewTestServer()
		require.NoError(t, err)
		defer server.Close()

		pingHandler := func(c *client.Client) {
			pingMessage := iso8583.NewMessage(testSpec)
			pingMessage.MTI("0800")
			pingMessage.Field(2, CardForPingCounter)

			response, err := c.Send(pingMessage)
			require.NoError(t, err)

			mti, err := response.GetMTI()
			require.NoError(t, err)
			require.Equal(t, "0810", mti)
		}

		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength,
			client.IdleTime(50*time.Millisecond),
			client.PingHandler(pingHandler),
		)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)
		defer c.Close()

		// we expect that ping interval in 50ms has not passed yet
		// and server has not being pinged
		require.Equal(t, 0, server.ReceivedPings)

		time.Sleep(200 * time.Millisecond)

		require.True(t, server.ReceivedPings > 0)
	})

	t.Run("it handles unrecognized responses", func(t *testing.T) {
		// unmatchedMessageHandler should be called for the second message
		// reply because client.Send will return ErrSendTimeout and
		// reply will not be handled by the original caller
		unmatchedMessageHandler := func(c *client.Client, message *iso8583.Message) {
			mti, err := message.GetMTI()
			require.NoError(t, err)
			require.Equal(t, "0810", mti)

			pan, err := message.GetString(2)
			require.NoError(t, err)
			require.Equal(t, CardForDelayedResponse, pan)
		}

		c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength,
			client.SendTimeout(100*time.Millisecond),
			client.UnmatchedMessageHandler(unmatchedMessageHandler),
		)
		require.NoError(t, err)

		err = c.Connect()
		require.NoError(t, err)
		defer c.Close()

		// network management message to test timeout
		message := iso8583.NewMessage(testSpec)
		message.MTI("0800")

		// using 777 value for the field, we tell server
		// to sleep for 500ms when process the message
		require.NoError(t, message.Field(2, CardForDelayedResponse))

		_, err = c.Send(message)
		require.Equal(t, client.ErrSendTimeout, err)

		// let's sleep here to wait for the server response to our message
		// we should not panic :)
		time.Sleep(1 * time.Second)
	})
}

func TestClient_SetOptions(t *testing.T) {
	c, err := client.NewClient("", testSpec, readMessageLength, writeMessageLength)
	require.NoError(t, err)
	require.NotNil(t, c)

	require.Nil(t, c.Opts.PingHandler)
	require.Nil(t, c.Opts.UnmatchedMessageHandler)
	require.Nil(t, c.Opts.TLSConfig)

	require.NoError(t, c.SetOptions(client.UnmatchedMessageHandler(func(c *client.Client, m *iso8583.Message) {})))
	require.NotNil(t, c.Opts.UnmatchedMessageHandler)

	require.NoError(t, c.SetOptions(client.PingHandler(func(c *client.Client) {})))
	require.NotNil(t, c.Opts.PingHandler)

	require.NoError(t, c.SetOptions(client.RootCAs("./testdata/ca.crt")))
	require.NotNil(t, c.Opts.TLSConfig)
}

func BenchmarkSend100(b *testing.B) { benchmarkSend(100, b) }

func BenchmarkSend1000(b *testing.B) { benchmarkSend(1000, b) }

func BenchmarkSend10000(b *testing.B) { benchmarkSend(10000, b) }

func BenchmarkSend100000(b *testing.B) { benchmarkSend(100000, b) }

func benchmarkSend(m int, b *testing.B) {
	server := server.New(testSpec, readMessageLength, writeMessageLength)
	// start on random port
	err := server.Start("127.0.0.1:")
	if err != nil {
		b.Fatal("starting server: ", err)
	}

	c, err := client.NewClient(server.Addr, testSpec, readMessageLength, writeMessageLength)
	if err != nil {
		b.Fatal("creating client: ", err)
	}

	err = c.Connect()
	if err != nil {
		b.Fatal("connecting to the server: ", err)
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		processMessages(b, m, c)
	}

	err = c.Close()
	if err != nil {
		b.Fatal("closing client: ", err)
	}
	server.Close()
}

// send/receive m messages
func processMessages(b *testing.B, m int, c *client.Client) {
	var wg sync.WaitGroup
	var gerr error

	for i := 0; i < m; i++ {
		wg.Add(1)
		go func() {
			defer func() {
				wg.Done()
			}()

			message := iso8583.NewMessage(testSpec)
			message.MTI("0800")

			_, err := c.Send(message)
			if err != nil {
				gerr = err
				return
			}
		}()
	}

	wg.Wait()
	if gerr != nil {
		b.Fatal("sending message: ", gerr)
	}
}
