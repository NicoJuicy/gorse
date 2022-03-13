// Copyright 2020 gorse Project Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/emicklei/go-restful/v3"
	"github.com/go-redis/redis/v8"
	"github.com/go-resty/resty/v2"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zhenghaoz/gorse/config"
	"github.com/zhenghaoz/gorse/storage/cache"
	"github.com/zhenghaoz/gorse/storage/data"
	"google.golang.org/protobuf/proto"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

const (
	numInitUsers    = 10000
	numInitItems    = 10000
	numInitFeedback = 10000
)

var (
	mysqlURL string
	redisURL string
)

func init() {
	// get environment variables
	env := func(key, defaultValue string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return defaultValue
	}
	mysqlURL = env("BENCH_MYSQL_URL", "root:password@tcp(127.0.0.1:3306)/")
	redisURL = env("BENCH_REDIS_URL", "redis://127.0.0.1:6379/8")
}

type benchServer struct {
	database    string
	dataClient  *sql.DB
	cacheClient *redis.Client
	listener    net.Listener
	address     string
	RestServer
}

func newBenchServer(b *testing.B, benchName string) *benchServer {
	// create database
	database := "gorse_" + benchName
	db, err := sql.Open("mysql", mysqlURL)
	assert.NoError(b, err)
	_, err = db.Exec("DROP DATABASE IF EXISTS " + database)
	assert.NoError(b, err)
	_, err = db.Exec("CREATE DATABASE " + database)
	assert.NoError(b, err)

	// clear redis
	opt, err := redis.ParseURL(redisURL)
	assert.NoError(b, err)
	cli := redis.NewClient(opt)
	assert.NoError(b, cli.FlushDB(context.Background()).Err())

	// configuration
	s := &benchServer{dataClient: db, cacheClient: cli, database: database}
	s.GorseConfig = (*config.Config)(nil).LoadDefaultIfNil()
	s.DisableLog = true
	s.WebService = new(restful.WebService)
	s.CreateWebService()
	container := restful.NewContainer()
	container.Add(s.WebService)

	// open database
	s.DataClient, err = data.Open("mysql://" + mysqlURL + database + "?timeout=30s&parseTime=true")
	assert.NoError(b, err)
	err = s.DataClient.Init()
	assert.NoError(b, err)
	s.CacheClient, err = cache.Open(redisURL)
	assert.NoError(b, err)

	// insert users
	users := make([]data.User, 0)
	for i := 0; i < numInitUsers; i++ {
		users = append(users, data.User{
			UserId: fmt.Sprintf("init_user_%d", i),
			Labels: []string{
				fmt.Sprintf("label%d001", i),
				fmt.Sprintf("label%d002", i),
			},
			Comment: fmt.Sprintf("add label for user: demo%d", i),
		})
	}
	err = s.DataClient.BatchInsertUsers(users)
	assert.NoError(b, err)

	// insert items
	items := make([]data.Item, 0)
	for i := 0; i < numInitItems; i++ {
		items = append(items, data.Item{
			ItemId: fmt.Sprintf("init_item_%d", i),
			Labels: []string{
				fmt.Sprintf("label%d001", i),
				fmt.Sprintf("label%d002", i),
			},
			Comment: fmt.Sprintf("add label for user: demo%d", i),
			Categories: []string{
				fmt.Sprintf("category%d001", i),
				fmt.Sprintf("category%d001", i),
			},
			Timestamp: time.Now(),
			IsHidden:  false,
		})
	}
	err = s.DataClient.BatchInsertItems(items)
	assert.NoError(b, err)

	// insert feedback
	feedbacks := make([]data.Feedback, 0)
	for i := 0; i < numInitFeedback; i++ {
		feedbacks = append(feedbacks, data.Feedback{
			FeedbackKey: data.FeedbackKey{
				FeedbackType: fmt.Sprintf("feedbackType:%d", i),
				ItemId:       fmt.Sprintf("init_item_%d", i),
				UserId:       fmt.Sprintf("init_user_%d", i),
			},
			Timestamp: time.Now(),
			Comment:   fmt.Sprintf("add feedback: %d", i),
		})
	}
	err = s.DataClient.BatchInsertFeedback(feedbacks, true, true, true)
	assert.NoError(b, err)

	// start http server
	s.listener, err = net.Listen("tcp", ":0")
	assert.NoError(b, err)
	s.address = fmt.Sprintf("http://127.0.0.1:%d", s.listener.Addr().(*net.TCPAddr).Port)
	go func() {
		err = http.Serve(s.listener, container)
		assert.NoError(b, err)
	}()
	return s
}

func (s *benchServer) Close(b *testing.B) {
	// drop database
	_, err := s.dataClient.Exec("DROP DATABASE " + s.database)
	assert.NoError(b, err)
	err = s.dataClient.Close()
	assert.NoError(b, err)

	// clear redis
	assert.NoError(b, s.cacheClient.FlushDB(context.Background()).Err())
	err = s.cacheClient.Close()
	assert.NoError(b, err)
}

func BenchmarkInsertUser(b *testing.B) {
	s := newBenchServer(b, "BenchmarkInsertUser")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.R().
			SetBody(&data.User{
				UserId: fmt.Sprintf("user_%d", i),
				Labels: []string{
					fmt.Sprintf("label%d001", i),
					fmt.Sprintf("label%d002", i),
				},
				Comment: fmt.Sprintf("add label for user: %d", i),
			}).
			Post(s.address + "/api/user")
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkPatchUser(b *testing.B) {
	s := newBenchServer(b, "BenchmarkPatchUser")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % numInitUsers
		times := i / numInitUsers
		r, err := client.R().
			SetBody(&data.UserPatch{
				Labels: []string{
					fmt.Sprintf("label%d001,updated %d times", index, times),
					fmt.Sprintf("label%d002,updated %d times", index, times),
				},
				Comment: proto.String(fmt.Sprintf("add label for user: %d, updated %d times", index, times)),
			}).
			Patch(s.address + "/api/user/init_user_" + strconv.Itoa(index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetUser(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetUser")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + "/api/user/init_user_" + strconv.Itoa(index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkInsertUsers(b *testing.B) {
	s := newBenchServer(b, "BenchmarkInsertUsers")
	defer s.Close(b)

	for batchSize := 10; batchSize <= 1000; batchSize *= 10 {
		b.Run(strconv.Itoa(batchSize), func(b *testing.B) {
			users := make([][]data.User, b.N)
			for i := 0; i < b.N; i++ {
				users[i] = make([]data.User, batchSize)
				for j := range users[i] {
					userId := fmt.Sprintf("batch_%d_user_%d", i, j)
					users[i][j] = data.User{
						UserId: fmt.Sprintf("batch_%d_user_%d", i, j),
						Labels: []string{
							fmt.Sprintf("label001 for user:%s", userId),
							fmt.Sprintf("label002 for user:%s", userId),
						},
						Comment: fmt.Sprintf("add label for user: %s", userId),
					}
				}
			}

			client := resty.New()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := client.R().
					SetBody(users[i]).
					Post(s.address + "/api/users")
				require.NoError(b, err)
				require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
			}
			b.StopTimer()
		})
	}
}

func BenchmarkGetUsers(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetUsers")
	defer s.Close(b)

	for batchSize := 10; batchSize <= 1000; batchSize *= 10 {
		b.Run(strconv.Itoa(batchSize), func(b *testing.B) {
			client := resty.New()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := client.R().
					Get(s.address + fmt.Sprintf("/api/users?n=%d", batchSize))
				require.NoError(b, err)
				require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
			}
			b.StopTimer()
		})
	}
}

func BenchmarkDeleteUser(b *testing.B) {
	s := newBenchServer(b, "BenchmarkDeleteUser")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.R().
			Delete(s.address + "/api/user/init_user_" + strconv.Itoa(i%numInitUsers))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

// item benchmark
func BenchmarkInsertItem(b *testing.B) {
	s := newBenchServer(b, "BenchmarkInsertItem")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		isHidden := false
		if i%2 == 0 {
			isHidden = true
		}
		r, err := client.R().
			SetBody(&data.Item{
				ItemId: strconv.Itoa(i),
				Labels: []string{
					fmt.Sprintf("label%d001", i),
					fmt.Sprintf("label%d002", i),
				},
				Categories: []string{
					fmt.Sprintf("category%d001", i),
					fmt.Sprintf("category%d001", i),
				},
				IsHidden:  isHidden,
				Timestamp: time.Now(),
				Comment:   fmt.Sprintf("add label for item: %d", i),
			}).
			Post(s.address + "/api/item")
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkPatchItem(b *testing.B) {
	s := newBenchServer(b, "BenchmarkPatchItem")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		times := i / 10000
		comment := fmt.Sprintf("add label for item: %d, updated %d times", index, times)
		isHidden := false
		if i%2 == 0 {
			isHidden = true
		}
		now := time.Now()
		r, err := client.R().
			SetBody(&data.ItemPatch{
				Labels: []string{
					fmt.Sprintf("label%d001,updated %d times", index, times),
					fmt.Sprintf("label%d002,updated %d times", index, times),
				},
				Comment: &comment,
				Categories: []string{
					fmt.Sprintf("category%d001, updated %d times", i, times),
					fmt.Sprintf("category%d001, updated %d times", i, times),
				},
				IsHidden:  &isHidden,
				Timestamp: &now,
			}).
			Patch(s.address + "/api/item/demo" + strconv.Itoa(index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetItem(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetItem")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.R().
			Get(s.address + "/api/item/init_item_" + strconv.Itoa(i%numInitItems))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkInsertItems(b *testing.B) {
	s := newBenchServer(b, "BenchmarkInsertItems")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		users := make([]data.Item, 0)
		for j := 0; j < (i+1)%1000; j++ {
			itemId := fmt.Sprintf("batch %d index %d", i, j)
			users = append(users, data.Item{
				ItemId: itemId,
				Labels: []string{
					fmt.Sprintf("label001 for item:%s", itemId),
					fmt.Sprintf("label002 for item:%s", itemId),
				},
				Comment: fmt.Sprintf("add label for item: %s", itemId),
			})
		}
		r, err := client.R().
			SetBody(users).
			Post(s.address + "/api/items")
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetItems(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetItems")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/items?n=%d", index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkDeleteItem(b *testing.B) {
	s := newBenchServer(b, "BenchmarkDeleteItem")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.R().
			Delete(s.address + "/api/item/init_item_" + strconv.Itoa(i%numInitItems))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

// category benchmark
func BenchmarkInsertCategory(b *testing.B) {
	s := newBenchServer(b, "BenchmarkInsertCategory")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.R().
			Put(fmt.Sprintf("%s/api/item/demo%d/category/category%d", s.address, i, i))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkDeleteCategory(b *testing.B) {
	s := newBenchServer(b, "BenchmarkDeleteCategory")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Delete(fmt.Sprintf("%s/api/item/demo%d/category/category%d", s.address, index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

// feedback benchmark
func BenchmarkPutFeedback(b *testing.B) {
	s := newBenchServer(b, "BenchmarkPutFeedback")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		feedbacks := make([]data.Feedback, 0)
		for j := 0; j < (i+1)%1000; j++ {
			feedbacks = append(feedbacks, data.Feedback{
				FeedbackKey: data.FeedbackKey{
					FeedbackType: fmt.Sprintf("feedbackType:%d", index),
					ItemId:       fmt.Sprintf("item:%d", index),
					UserId:       fmt.Sprintf("user:%d", index),
				},
				Timestamp: time.Now(),
				Comment:   fmt.Sprintf("add feedback: %d", index),
			})
		}
		r, err := client.R().
			SetBody(feedbacks).
			Put(s.address + "/api/feedback")
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkInsertFeedback(b *testing.B) {
	s := newBenchServer(b, "BenchmarkInsertFeedback")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		feedbacks := make([]data.Feedback, 0)
		for j := 0; j < (i+1)%1000; j++ {
			feedbacks = append(feedbacks, data.Feedback{
				FeedbackKey: data.FeedbackKey{
					FeedbackType: fmt.Sprintf("feedbackType:%d", i),
					ItemId:       fmt.Sprintf("item:%d", i),
					UserId:       fmt.Sprintf("user:%d", i),
				},
				Timestamp: time.Now(),
				Comment:   fmt.Sprintf("add feedback: %d", i),
			})
		}
		r, err := client.R().
			SetBody(feedbacks).
			Post(s.address + "/api/feedback")
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedback(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedback")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/feedback?n=%d", index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByUserIdAndItemId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByUserIdAndItemId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/feedback/user:%d/item:%d", index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkDeleteFeedbackByUserIdAndItemId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkDeleteFeedbackByUserIdAndItemId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Delete(s.address + fmt.Sprintf("/api/feedback/user:%d/item:%d", index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByFeedbackType(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByFeedbackType")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/feedback/feedbackType:%d", index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkDeleteFeedbackByFeedbackTypeAndUserIdAndItemId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkDeleteFeedbackByFeedbackTypeAndUserIdAndItemId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Delete(s.address + fmt.Sprintf("/api/feedback/feedbackType:%d/user:%d/item:%d", index, index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByFeedbackTypeAndUserIdAndItemId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByFeedbackTypeAndUserIdAndItemId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/feedback/feedbackType:%d/user:%d/item:%d", index, index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByFeedbackTypeAndUserId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByFeedbackTypeAndUserId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/user/user:%d/feedback/feedbackType:%d", index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByUserId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByUserId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/user/user:%d/feedback", index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByFeedbackTypeAndItemId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByFeedbackTypeAndItemId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/item/item:%d/feedback/feedbackType:%d", index, index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

func BenchmarkGetFeedbackByItemId(b *testing.B) {
	s := newBenchServer(b, "BenchmarkGetFeedbackByItemId")
	defer s.Close(b)

	client := resty.New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index := i % 10000
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/item/item:%d/feedback", index))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}

// get list
func BenchmarkGetList(b *testing.B) {

	s := newBenchServer(b, "BenchmarkGetFeedbackByItemId")
	defer s.Close(b)

	client := resty.New()
	err := s.CacheClient.SetSorted(cache.PopularItems,
		[]cache.Scored{{"9", 91}, {"10", 90}, {"11", 89}, {"12", 88}})
	require.NoError(b, err)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := client.R().
			Get(s.address + fmt.Sprintf("/api/popular"))
		require.NoError(b, err)
		require.Equal(b, http.StatusOK, r.StatusCode(), r.String())
	}
	b.StopTimer()
}
