package utils

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/SSHZ-ORG/dedicatus/config"
	"golang.org/x/net/context"
	"google.golang.org/api/kgsearch/v1"
	"google.golang.org/api/option"
	"google.golang.org/appengine/log"
	"google.golang.org/appengine/memcache"
)

const kgMemcacheKey = "KG1:"

// this returns the `result` node of the found entity.
func sendKGEntityQuery(ctx context.Context, query string) (map[string]interface{}, error) {
	s, err := kgsearch.NewService(ctx, option.WithAPIKey(config.KGAPIKey))
	if err != nil {
		return nil, err
	}

	req := kgsearch.NewEntitiesService(s).Search().Query(query).Languages("ja", "zh").Types("Person").Limit(1)
	resp, err := req.Do()
	if err != nil {
		return nil, err
	}

	if len(resp.ItemListElement) > 0 {
		return resp.ItemListElement[0].(map[string]interface{})["result"].(map[string]interface{}), nil
	}
	return nil, nil
}

func tryFindKGEntityInternal(ctx context.Context, query string) (string, error) {
	result, err := sendKGEntityQuery(ctx, query)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return strings.TrimPrefix(result["@id"].(string), "kg:"), nil
}

func getKGMemcacheKey(query string) string {
	return kgMemcacheKey + query
}

func getKGMemcache(ctx context.Context, query string) *string {
	item, err := memcache.Get(ctx, getKGMemcacheKey(query))
	if err == nil {
		s := string(item.Value)
		return &s
	}
	return nil
}

func setKGMemcache(ctx context.Context, query, result string) {
	_ = memcache.Set(ctx, &memcache.Item{
		Key:        getKGMemcacheKey(query),
		Value:      []byte(result),
		Expiration: 4 * time.Hour,
	})
}

func TryFindKGEntity(ctx context.Context, query string) string {
	resultFromMemcache := getKGMemcache(ctx, query)
	if resultFromMemcache != nil {
		return *resultFromMemcache
	}

	result, err := tryFindKGEntityInternal(ctx, query)
	if err != nil {
		// Don't fail the query, just log and return empty result.
		log.Warningf(ctx, "tryFindKGEntityInternal: %v", err)
		return ""
	}

	setKGMemcache(ctx, query, result)
	return result
}

func GetKGQueryResult(ctx context.Context, query string) (string, error) {
	// This bypasses memcache
	result, err := sendKGEntityQuery(ctx, query)
	if err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(result, "", "    ")
	return string(encoded), err
}
