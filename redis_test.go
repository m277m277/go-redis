package redis_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	. "github.com/bsm/ginkgo/v2"
	. "github.com/bsm/gomega"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/auth"
)

type redisHookError struct{}

var _ redis.Hook = redisHookError{}

func (redisHookError) DialHook(hook redis.DialHook) redis.DialHook {
	return hook
}

func (redisHookError) ProcessHook(hook redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		return errors.New("hook error")
	}
}

func (redisHookError) ProcessPipelineHook(hook redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return hook
}

func TestHookError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: ":6379",
	})
	rdb.AddHook(redisHookError{})

	err := rdb.Ping(ctx).Err()
	if err == nil {
		t.Fatalf("got nil, expected an error")
	}

	wanted := "hook error"
	if err.Error() != wanted {
		t.Fatalf(`got %q, wanted %q`, err, wanted)
	}
}

//------------------------------------------------------------------------------

var _ = Describe("Client", func() {
	var client *redis.Client

	BeforeEach(func() {
		client = redis.NewClient(redisOptions())
		Expect(client.FlushDB(ctx).Err()).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		client.Close()
	})

	It("should Stringer", func() {
		Expect(client.String()).To(Equal(fmt.Sprintf("Redis<:%s db:0>", redisPort)))
	})

	It("supports context", func() {
		ctx, cancel := context.WithCancel(ctx)
		cancel()

		err := client.Ping(ctx).Err()
		Expect(err).To(MatchError("context canceled"))
	})

	It("supports WithTimeout", Label("NonRedisEnterprise"), func() {
		err := client.ClientPause(ctx, time.Second).Err()
		Expect(err).NotTo(HaveOccurred())

		err = client.WithTimeout(10 * time.Millisecond).Ping(ctx).Err()
		Expect(err).To(HaveOccurred())

		err = client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())
	})

	It("do", func() {
		val, err := client.Do(ctx, "ping").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal("PONG"))
	})

	It("should ping", func() {
		val, err := client.Ping(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal("PONG"))
	})

	It("should return pool stats", func() {
		Expect(client.PoolStats()).To(BeAssignableToTypeOf(&redis.PoolStats{}))
	})

	It("should support custom dialers", func() {
		custom := redis.NewClient(&redis.Options{
			Network: "tcp",
			Addr:    redisAddr,
			Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		})

		val, err := custom.Ping(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).To(Equal("PONG"))
		Expect(custom.Close()).NotTo(HaveOccurred())
	})

	It("should close", func() {
		Expect(client.Close()).NotTo(HaveOccurred())
		err := client.Ping(ctx).Err()
		Expect(err).To(MatchError("redis: client is closed"))
	})

	It("should close pubsub without closing the client", func() {
		pubsub := client.Subscribe(ctx)
		Expect(pubsub.Close()).NotTo(HaveOccurred())

		_, err := pubsub.Receive(ctx)
		Expect(err).To(MatchError("redis: client is closed"))
		Expect(client.Ping(ctx).Err()).NotTo(HaveOccurred())
	})

	It("should close Tx without closing the client", func() {
		err := client.Watch(ctx, func(tx *redis.Tx) error {
			_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Ping(ctx)
				return nil
			})
			return err
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(client.Ping(ctx).Err()).NotTo(HaveOccurred())
	})

	It("should close pubsub when client is closed", func() {
		pubsub := client.Subscribe(ctx)
		Expect(client.Close()).NotTo(HaveOccurred())

		_, err := pubsub.Receive(ctx)
		Expect(err).To(MatchError("redis: client is closed"))

		Expect(pubsub.Close()).NotTo(HaveOccurred())
	})

	It("should select DB", Label("NonRedisEnterprise"), func() {
		db2 := redis.NewClient(&redis.Options{
			Addr: redisAddr,
			DB:   2,
		})
		Expect(db2.FlushDB(ctx).Err()).NotTo(HaveOccurred())
		Expect(db2.Get(ctx, "db").Err()).To(Equal(redis.Nil))
		Expect(db2.Set(ctx, "db", 2, 0).Err()).NotTo(HaveOccurred())

		n, err := db2.Get(ctx, "db").Int64()
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(2)))

		Expect(client.Get(ctx, "db").Err()).To(Equal(redis.Nil))

		Expect(db2.FlushDB(ctx).Err()).NotTo(HaveOccurred())
		Expect(db2.Close()).NotTo(HaveOccurred())
	})

	It("should client setname", func() {
		opt := redisOptions()
		opt.ClientName = "hi"
		db := redis.NewClient(opt)

		defer func() {
			Expect(db.Close()).NotTo(HaveOccurred())
		}()

		Expect(db.Ping(ctx).Err()).NotTo(HaveOccurred())
		val, err := db.ClientList(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).Should(ContainSubstring("name=hi"))
	})

	It("should attempt to set client name in HELLO", func() {
		opt := redisOptions()
		opt.ClientName = "hi"
		db := redis.NewClient(opt)

		defer func() {
			Expect(db.Close()).NotTo(HaveOccurred())
		}()

		// Client name should be already set on any successfully initialized connection
		name, err := db.ClientGetName(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(name).Should(Equal("hi"))

		// HELLO should be able to explicitly overwrite the client name
		conn := db.Conn()
		hello, err := conn.Hello(ctx, 3, "", "", "hi2").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(hello["proto"]).Should(Equal(int64(3)))
		name, err = conn.ClientGetName(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(name).Should(Equal("hi2"))
		err = conn.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	It("should client PROTO 2", func() {
		opt := redisOptions()
		opt.Protocol = 2
		db := redis.NewClient(opt)

		defer func() {
			Expect(db.Close()).NotTo(HaveOccurred())
		}()

		val, err := db.Do(ctx, "HELLO").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).Should(ContainElements("proto", int64(2)))
	})

	It("should client PROTO 3", func() {
		opt := redisOptions()
		db := redis.NewClient(opt)

		defer func() {
			Expect(db.Close()).NotTo(HaveOccurred())
		}()

		val, err := db.Do(ctx, "HELLO").Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(val).Should(HaveKeyWithValue("proto", int64(3)))
	})

	It("processes custom commands", func() {
		cmd := redis.NewCmd(ctx, "PING")
		_ = client.Process(ctx, cmd)

		// Flush buffers.
		Expect(client.Echo(ctx, "hello").Err()).NotTo(HaveOccurred())

		Expect(cmd.Err()).NotTo(HaveOccurred())
		Expect(cmd.Val()).To(Equal("PONG"))
	})

	It("should retry command on network error", func() {
		Expect(client.Close()).NotTo(HaveOccurred())

		client = redis.NewClient(&redis.Options{
			Addr:       redisAddr,
			MaxRetries: 1,
		})

		// Put bad connection in the pool.
		cn, err := client.Pool().Get(ctx)
		Expect(err).NotTo(HaveOccurred())

		cn.SetNetConn(&badConn{})
		client.Pool().Put(ctx, cn)

		err = client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())
	})

	It("should retry with backoff", func() {
		clientNoRetry := redis.NewClient(&redis.Options{
			Addr:       ":1234",
			MaxRetries: -1,
		})
		defer clientNoRetry.Close()

		clientRetry := redis.NewClient(&redis.Options{
			Addr:            ":1234",
			MaxRetries:      5,
			MaxRetryBackoff: 128 * time.Millisecond,
		})
		defer clientRetry.Close()

		startNoRetry := time.Now()
		err := clientNoRetry.Ping(ctx).Err()
		Expect(err).To(HaveOccurred())
		elapseNoRetry := time.Since(startNoRetry)

		startRetry := time.Now()
		err = clientRetry.Ping(ctx).Err()
		Expect(err).To(HaveOccurred())
		elapseRetry := time.Since(startRetry)

		Expect(elapseRetry).To(BeNumerically(">", elapseNoRetry, 10*time.Millisecond))
	})

	It("should update conn.UsedAt on read/write", func() {
		cn, err := client.Pool().Get(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(cn.UsedAt).NotTo(BeZero())

		// set cn.SetUsedAt(time) or time.Sleep(>1*time.Second)
		// simulate the last time Conn was used
		// time.Sleep() is not the standard sleep time
		// link: https://go-review.googlesource.com/c/go/+/232298
		cn.SetUsedAt(time.Now().Add(-1 * time.Second))
		createdAt := cn.UsedAt()

		client.Pool().Put(ctx, cn)
		Expect(cn.UsedAt().Equal(createdAt)).To(BeTrue())

		err = client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())

		cn, err = client.Pool().Get(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(cn).NotTo(BeNil())
		Expect(cn.UsedAt().After(createdAt)).To(BeTrue())
	})

	It("should process command with special chars", func() {
		set := client.Set(ctx, "key", "hello1\r\nhello2\r\n", 0)
		Expect(set.Err()).NotTo(HaveOccurred())
		Expect(set.Val()).To(Equal("OK"))

		get := client.Get(ctx, "key")
		Expect(get.Err()).NotTo(HaveOccurred())
		Expect(get.Val()).To(Equal("hello1\r\nhello2\r\n"))
	})

	It("should handle big vals", func() {
		bigVal := bytes.Repeat([]byte{'*'}, 2e6)

		err := client.Set(ctx, "key", bigVal, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		// Reconnect to get new connection.
		Expect(client.Close()).NotTo(HaveOccurred())
		client = redis.NewClient(redisOptions())

		got, err := client.Get(ctx, "key").Bytes()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(bigVal))
	})

	It("should set and scan time", func() {
		tm := time.Now()
		err := client.Set(ctx, "now", tm, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		var tm2 time.Time
		err = client.Get(ctx, "now").Scan(&tm2)
		Expect(err).NotTo(HaveOccurred())

		Expect(tm2).To(BeTemporally("==", tm))
	})

	It("should set and scan durations", func() {
		duration := 10 * time.Minute
		err := client.Set(ctx, "duration", duration, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		var duration2 time.Duration
		err = client.Get(ctx, "duration").Scan(&duration2)
		Expect(err).NotTo(HaveOccurred())

		Expect(duration2).To(Equal(duration))
	})

	It("should Conn", func() {
		err := client.Conn().Get(ctx, "this-key-does-not-exist").Err()
		Expect(err).To(Equal(redis.Nil))
	})

	It("should set and scan net.IP", func() {
		ip := net.ParseIP("192.168.1.1")
		err := client.Set(ctx, "ip", ip, 0).Err()
		Expect(err).NotTo(HaveOccurred())

		var ip2 net.IP
		err = client.Get(ctx, "ip").Scan(&ip2)
		Expect(err).NotTo(HaveOccurred())

		Expect(ip2).To(Equal(ip))
	})
})

var _ = Describe("Client timeout", func() {
	var opt *redis.Options
	var client *redis.Client

	AfterEach(func() {
		Expect(client.Close()).NotTo(HaveOccurred())
	})

	testTimeout := func() {
		It("SETINFO timeouts", func() {
			conn := client.Conn()
			err := conn.Ping(ctx).Err()
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Ping timeouts", func() {
			err := client.Ping(ctx).Err()
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Pipeline timeouts", func() {
			_, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Ping(ctx)
				return nil
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Subscribe timeouts", func() {
			if opt.WriteTimeout == 0 {
				return
			}

			pubsub := client.Subscribe(ctx)
			defer pubsub.Close()

			err := pubsub.Subscribe(ctx, "_")
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Tx timeouts", func() {
			err := client.Watch(ctx, func(tx *redis.Tx) error {
				return tx.Ping(ctx).Err()
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})

		It("Tx Pipeline timeouts", func() {
			err := client.Watch(ctx, func(tx *redis.Tx) error {
				_, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
					pipe.Ping(ctx)
					return nil
				})
				return err
			})
			Expect(err).To(HaveOccurred())
			Expect(err.(net.Error).Timeout()).To(BeTrue())
		})
	}

	Context("read timeout", func() {
		BeforeEach(func() {
			opt = redisOptions()
			opt.ReadTimeout = time.Nanosecond
			opt.WriteTimeout = -1
			client = redis.NewClient(opt)
		})

		testTimeout()
	})

	Context("write timeout", func() {
		BeforeEach(func() {
			opt = redisOptions()
			opt.ReadTimeout = -1
			opt.WriteTimeout = time.Nanosecond
			client = redis.NewClient(opt)
		})

		testTimeout()
	})
})

var _ = Describe("Client OnConnect", func() {
	var client *redis.Client

	BeforeEach(func() {
		opt := redisOptions()
		opt.DB = 0
		opt.OnConnect = func(ctx context.Context, cn *redis.Conn) error {
			return cn.ClientSetName(ctx, "on_connect").Err()
		}

		client = redis.NewClient(opt)
	})

	AfterEach(func() {
		Expect(client.Close()).NotTo(HaveOccurred())
	})

	It("calls OnConnect", func() {
		name, err := client.ClientGetName(ctx).Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("on_connect"))
	})
})

var _ = Describe("Client context cancellation", func() {
	var opt *redis.Options
	var client *redis.Client

	BeforeEach(func() {
		opt = redisOptions()
		opt.ReadTimeout = -1
		opt.WriteTimeout = -1
		client = redis.NewClient(opt)
	})

	AfterEach(func() {
		Expect(client.Close()).NotTo(HaveOccurred())
	})

	It("Blocking operation cancellation", func() {
		ctx, cancel := context.WithCancel(ctx)
		cancel()

		err := client.BLPop(ctx, 1*time.Second, "test").Err()
		Expect(err).To(HaveOccurred())
		Expect(err).To(BeIdenticalTo(context.Canceled))
	})
})

var _ = Describe("Conn", func() {
	var client *redis.Client

	BeforeEach(func() {
		client = redis.NewClient(redisOptions())
		Expect(client.FlushDB(ctx).Err()).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err := client.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	It("TxPipeline", Label("NonRedisEnterprise"), func() {
		tx := client.Conn().TxPipeline()
		tx.SwapDB(ctx, 0, 2)
		tx.SwapDB(ctx, 1, 0)
		_, err := tx.Exec(ctx)
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("Hook", func() {
	var client *redis.Client

	BeforeEach(func() {
		client = redis.NewClient(redisOptions())
		Expect(client.FlushDB(ctx).Err()).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err := client.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	It("fifo", func() {
		var res []string
		client.AddHook(&hook{
			processHook: func(hook redis.ProcessHook) redis.ProcessHook {
				return func(ctx context.Context, cmd redis.Cmder) error {
					res = append(res, "hook-1-process-start")
					err := hook(ctx, cmd)
					res = append(res, "hook-1-process-end")
					return err
				}
			},
		})
		client.AddHook(&hook{
			processHook: func(hook redis.ProcessHook) redis.ProcessHook {
				return func(ctx context.Context, cmd redis.Cmder) error {
					res = append(res, "hook-2-process-start")
					err := hook(ctx, cmd)
					res = append(res, "hook-2-process-end")
					return err
				}
			},
		})

		err := client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())

		Expect(res).To(Equal([]string{
			"hook-1-process-start",
			"hook-2-process-start",
			"hook-2-process-end",
			"hook-1-process-end",
		}))
	})

	It("wrapped error in a hook", func() {
		client.AddHook(&hook{
			processHook: func(hook redis.ProcessHook) redis.ProcessHook {
				return func(ctx context.Context, cmd redis.Cmder) error {
					if err := hook(ctx, cmd); err != nil {
						return fmt.Errorf("wrapped error: %w", err)
					}
					return nil
				}
			},
		})
		client.ScriptFlush(ctx)

		script := redis.NewScript(`return 'Script and hook'`)

		cmd := script.Run(ctx, client, nil)
		Expect(cmd.Err()).NotTo(HaveOccurred())
		Expect(cmd.Val()).To(Equal("Script and hook"))
	})
})

var _ = Describe("Hook with MinIdleConns", func() {
	var client *redis.Client

	BeforeEach(func() {
		options := redisOptions()
		options.MinIdleConns = 1
		client = redis.NewClient(options)
		Expect(client.FlushDB(ctx).Err()).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		err := client.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	It("fifo", func() {
		var res []string
		client.AddHook(&hook{
			processHook: func(hook redis.ProcessHook) redis.ProcessHook {
				return func(ctx context.Context, cmd redis.Cmder) error {
					res = append(res, "hook-1-process-start")
					err := hook(ctx, cmd)
					res = append(res, "hook-1-process-end")
					return err
				}
			},
		})
		client.AddHook(&hook{
			processHook: func(hook redis.ProcessHook) redis.ProcessHook {
				return func(ctx context.Context, cmd redis.Cmder) error {
					res = append(res, "hook-2-process-start")
					err := hook(ctx, cmd)
					res = append(res, "hook-2-process-end")
					return err
				}
			},
		})

		err := client.Ping(ctx).Err()
		Expect(err).NotTo(HaveOccurred())

		Expect(res).To(Equal([]string{
			"hook-1-process-start",
			"hook-2-process-start",
			"hook-2-process-end",
			"hook-1-process-end",
		}))
	})
})

var _ = Describe("Dialer connection timeouts", func() {
	var client *redis.Client

	const dialSimulatedDelay = 1 * time.Second

	BeforeEach(func() {
		options := redisOptions()
		options.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Simulated slow dialer.
			// Note that the following sleep is deliberately not context-aware.
			time.Sleep(dialSimulatedDelay)
			return net.Dial("tcp", options.Addr)
		}
		options.MinIdleConns = 1
		client = redis.NewClient(options)
	})

	AfterEach(func() {
		err := client.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not contend on connection dial for concurrent commands", func() {
		var wg sync.WaitGroup

		const concurrency = 10

		durations := make(chan time.Duration, concurrency)
		errs := make(chan error, concurrency)

		start := time.Now()
		wg.Add(concurrency)

		for i := 0; i < concurrency; i++ {
			go func() {
				defer wg.Done()

				start := time.Now()
				err := client.Ping(ctx).Err()
				durations <- time.Since(start)
				errs <- err
			}()
		}

		wg.Wait()
		close(durations)
		close(errs)

		// All commands should eventually succeed, after acquiring a connection.
		for err := range errs {
			Expect(err).NotTo(HaveOccurred())
		}

		// Each individual command should complete within the simulated dial duration bound.
		for duration := range durations {
			Expect(duration).To(BeNumerically("<", 2*dialSimulatedDelay))
		}

		// Due to concurrent execution, the entire test suite should also complete within
		// the same dial duration bound applied for individual commands.
		Expect(time.Since(start)).To(BeNumerically("<", 2*dialSimulatedDelay))
	})
})

var _ = Describe("Credentials Provider Priority", func() {
	var client *redis.Client
	var opt *redis.Options
	var recorder *commandRecorder

	BeforeEach(func() {
		recorder = newCommandRecorder(10)
	})

	AfterEach(func() {
		if client != nil {
			Expect(client.Close()).NotTo(HaveOccurred())
		}
	})

	It("should use streaming provider when available", func() {
		streamingCreds := auth.NewBasicCredentials("streaming_user", "streaming_pass")
		ctxCreds := auth.NewBasicCredentials("ctx_user", "ctx_pass")
		providerCreds := auth.NewBasicCredentials("provider_user", "provider_pass")

		opt = &redis.Options{
			Username: "field_user",
			Password: "field_pass",
			CredentialsProvider: func() (string, string) {
				username, password := providerCreds.BasicAuth()
				return username, password
			},
			CredentialsProviderContext: func(ctx context.Context) (string, string, error) {
				username, password := ctxCreds.BasicAuth()
				return username, password, nil
			},
			StreamingCredentialsProvider: &mockStreamingProvider{
				credentials: streamingCreds,
				updates:     make(chan auth.Credentials, 1),
			},
		}

		client = redis.NewClient(opt)
		client.AddHook(recorder.Hook())
		// wrongpass
		Expect(client.Ping(context.Background()).Err()).To(HaveOccurred())
		Expect(recorder.Contains("AUTH streaming_user")).To(BeTrue())
	})

	It("should use context provider when streaming provider is not available", func() {
		ctxCreds := auth.NewBasicCredentials("ctx_user", "ctx_pass")
		providerCreds := auth.NewBasicCredentials("provider_user", "provider_pass")

		opt = &redis.Options{
			Username: "field_user",
			Password: "field_pass",
			CredentialsProvider: func() (string, string) {
				username, password := providerCreds.BasicAuth()
				return username, password
			},
			CredentialsProviderContext: func(ctx context.Context) (string, string, error) {
				username, password := ctxCreds.BasicAuth()
				return username, password, nil
			},
		}

		client = redis.NewClient(opt)
		client.AddHook(recorder.Hook())
		// wrongpass
		Expect(client.Ping(context.Background()).Err()).To(HaveOccurred())
		Expect(recorder.Contains("AUTH ctx_user")).To(BeTrue())
	})

	It("should use regular provider when streaming and context providers are not available", func() {
		providerCreds := auth.NewBasicCredentials("provider_user", "provider_pass")

		opt = &redis.Options{
			Username: "field_user",
			Password: "field_pass",
			CredentialsProvider: func() (string, string) {
				username, password := providerCreds.BasicAuth()
				return username, password
			},
		}

		client = redis.NewClient(opt)
		client.AddHook(recorder.Hook())
		// wrongpass
		Expect(client.Ping(context.Background()).Err()).To(HaveOccurred())
		Expect(recorder.Contains("AUTH provider_user")).To(BeTrue())
	})

	It("should use username/password fields when no providers are set", func() {
		opt = &redis.Options{
			Username: "field_user",
			Password: "field_pass",
		}

		client = redis.NewClient(opt)
		client.AddHook(recorder.Hook())
		// wrongpass
		Expect(client.Ping(context.Background()).Err()).To(HaveOccurred())
		Expect(recorder.Contains("AUTH field_user")).To(BeTrue())
	})

	It("should use empty credentials when nothing is set", func() {
		opt = &redis.Options{}

		client = redis.NewClient(opt)
		client.AddHook(recorder.Hook())
		// no pass, ok
		Expect(client.Ping(context.Background()).Err()).NotTo(HaveOccurred())
		Expect(recorder.Contains("AUTH")).To(BeFalse())
	})

	It("should handle credential updates from streaming provider", func() {
		initialCreds := auth.NewBasicCredentials("initial_user", "initial_pass")
		updatedCreds := auth.NewBasicCredentials("updated_user", "updated_pass")
		updatesChan := make(chan auth.Credentials, 1)

		opt = &redis.Options{
			StreamingCredentialsProvider: &mockStreamingProvider{
				credentials: initialCreds,
				updates:     updatesChan,
			},
		}

		client = redis.NewClient(opt)
		client.AddHook(recorder.Hook())
		// wrongpass
		Expect(client.Ping(context.Background()).Err()).To(HaveOccurred())
		Expect(recorder.Contains("AUTH initial_user")).To(BeTrue())

		// Update credentials
		opt.StreamingCredentialsProvider.(*mockStreamingProvider).updates <- updatedCreds
		// wrongpass
		Expect(client.Ping(context.Background()).Err()).To(HaveOccurred())
		Expect(recorder.Contains("AUTH updated_user")).To(BeTrue())
		close(updatesChan)
	})
})

type mockStreamingProvider struct {
	credentials auth.Credentials
	err         error
	updates     chan auth.Credentials
}

func (m *mockStreamingProvider) Subscribe(listener auth.CredentialsListener) (auth.Credentials, auth.UnsubscribeFunc, error) {
	if m.err != nil {
		return nil, nil, m.err
	}

	// Start goroutine to handle updates
	go func() {
		for creds := range m.updates {
			m.credentials = creds
			listener.OnNext(creds)
		}
	}()

	return m.credentials, func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				// this is just a mock:
				// allow multiple closes from multiple listeners
			}
		}()
		return
	}, nil
}

var _ = Describe("Client creation", func() {
	Context("simple client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewClient(nil)
			}).To(Panic())
		})
	})
	Context("cluster client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewClusterClient(nil)
			}).To(Panic())
		})
	})
	Context("ring client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewRing(nil)
			}).To(Panic())
		})
	})
	Context("universal client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewUniversalClient(nil)
			}).To(Panic())
		})
	})
	Context("failover client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewFailoverClient(nil)
			}).To(Panic())
		})
	})
	Context("failover cluster client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewFailoverClusterClient(nil)
			}).To(Panic())
		})
	})
	Context("sentinel client with nil options", func() {
		It("panics", func() {
			Expect(func() {
				redis.NewSentinelClient(nil)
			}).To(Panic())
		})
	})
})
