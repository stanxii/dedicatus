package models

import (
	"crypto/md5"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SSHZ-ORG/dedicatus/tgapi"
	"github.com/SSHZ-ORG/dedicatus/utils"
	"github.com/qedus/nds"
	"golang.org/x/net/context"
	"google.golang.org/appengine"
	"google.golang.org/appengine/datastore"
	"google.golang.org/appengine/log"
)

const maxItems = 50

var (
	ErrorOnlyAdminCanUpdateInventory = errors.New("Only admins can update an existing GIF.")
)

type Inventory struct {
	FileID      string
	FileType    string
	Personality []*datastore.Key
	Creator     int

	UsageCount int64
	LastUsed   time.Time

	MD5Sum   datastore.ByteString
	FileSize int
}

func (i Inventory) ToString(ctx context.Context) (string, error) {
	ps := make([]*Personality, len(i.Personality))
	err := nds.GetMulti(ctx, i.Personality, ps)
	if err != nil {
		return "", err
	}

	var pns []string
	for _, p := range ps {
		pns = append(pns, p.CanonicalName)
	}

	return fmt.Sprintf("%s [%s]", i.FileID, strings.Join(pns, ", ")), nil
}

func inventoryKey(ctx context.Context, fileID string) *datastore.Key {
	return datastore.NewKey(ctx, inventoryEntityKind, fileID, 0, nil)
}

func GetInventory(ctx context.Context, fileID string) (*Inventory, error) {
	i := new(Inventory)
	key := inventoryKey(ctx, fileID)
	err := nds.Get(ctx, key, i)
	return i, err
}

func GetInventoryByFile(ctx context.Context, fileID string, fileSize int) (*Inventory, error) {
	count, err := datastore.NewQuery(inventoryEntityKind).Filter("FileSize =", fileSize).Count(ctx)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	_, b, err := tgapi.FetchFileInfo(ctx, fileID)
	s := md5.Sum(b)

	keys, err := datastore.NewQuery(inventoryEntityKind).Filter("MD5Sum =", s[:]).KeysOnly().GetAll(ctx, nil)
	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		return nil, nil
	} else if len(keys) > 1 {
		log.Criticalf(ctx, "Hash conflict (%x)!", s)
		return nil, nil
	}

	i := new(Inventory)
	err = nds.Get(ctx, keys[0], i)
	return i, err
}

func CreateInventory(ctx context.Context, fileID string, personality []*datastore.Key, userID int, config Config) (*Inventory, error) {
	i := new(Inventory)
	key := inventoryKey(ctx, fileID)
	err := nds.Get(ctx, key, i)

	// This is an existing Inventory, only admins or original creator can update it.
	if err == nil && !(config.IsAdmin(userID) || i.Creator == userID) {
		return nil, ErrorOnlyAdminCanUpdateInventory
	}
	if err != nil && err != datastore.ErrNoSuchEntity {
		return nil, err
	}

	i.FileID = fileID
	i.FileType = utils.FileTypeMPEG4GIF
	i.Personality = personality
	i.LastUsed = time.Now()

	if i.Creator == 0 {
		i.Creator = userID
	}

	_, err = nds.Put(ctx, key, i)
	return i, err
}

func FindInventories(ctx context.Context, personalities []*datastore.Key, lastCursor string) ([]*Inventory, string, error) {
	q := datastore.NewQuery(inventoryEntityKind).KeysOnly()

	for _, personality := range personalities {
		q = q.Filter("Personality = ", personality)
	}

	q = q.Order("-UsageCount").Limit(maxItems)

	offset, err := strconv.Atoi(lastCursor)
	if err != nil {
		q = q.Offset(offset)
	}

	keys, err := q.GetAll(ctx, nil)
	if err != nil {
		return nil, "", err
	}

	if len(keys) == 0 {
		return nil, "", nil
	}

	inventories := make([]*Inventory, len(keys))
	err = nds.GetMulti(ctx, keys, inventories)
	if err != nil {
		return nil, "", err
	}

	newCursor := ""
	if len(keys) == maxItems {
		newCursor = strconv.Itoa(offset + maxItems)
	}

	return inventories, newCursor, nil
}

func GloballyLastUsedInventories(ctx context.Context) ([]*Inventory, error) {
	keys, err := datastore.NewQuery(inventoryEntityKind).KeysOnly().Order("-LastUsed").Limit(maxItems).GetAll(ctx, nil)
	if err != nil {
		return nil, err
	}

	if len(keys) == 0 {
		return nil, nil
	}

	inventories := make([]*Inventory, len(keys))
	err = nds.GetMulti(ctx, keys, inventories)
	return inventories, err
}

func AllInventoriesFileIDs(ctx context.Context) ([]string, error) {
	keys, err := datastore.NewQuery(inventoryEntityKind).KeysOnly().GetAll(ctx, nil)
	if err != nil {
		return nil, err
	}

	var fileIDs []string
	for _, k := range keys {
		fileIDs = append(fileIDs, k.StringID())
	}

	return fileIDs, nil
}

func IncrementUsageCounter(ctx context.Context, fileID string) error {
	return nds.RunInTransaction(ctx, func(ctx context.Context) error {
		i := new(Inventory)
		key := inventoryKey(ctx, fileID)
		if err := nds.Get(ctx, key, i); err != nil {
			if err == datastore.ErrNoSuchEntity {
				// Silently ignore this.
				return nil
			}
			return err
		}

		i.UsageCount += 1
		i.LastUsed = time.Now()

		_, err := nds.Put(ctx, key, i)
		return err
	}, &datastore.TransactionOptions{})
}

func CountInventories(ctx context.Context, personality *datastore.Key) (int, error) {
	return datastore.NewQuery(inventoryEntityKind).KeysOnly().Filter("Personality = ", personality).Count(ctx)
}

func ReplaceFileID(ctx context.Context, oldFileID, newFileID string) (*Inventory, error) {
	i := new(Inventory)

	err := nds.RunInTransaction(ctx, func(ctx context.Context) error {
		oldKey := inventoryKey(ctx, oldFileID)
		if err := nds.Get(ctx, oldKey, i); err != nil {
			return err
		}

		i.FileID = newFileID

		if err := nds.Delete(ctx, oldKey); err != nil {
			return err
		}
		_, err := nds.Put(ctx, inventoryKey(ctx, newFileID), i)
		return err
	}, &datastore.TransactionOptions{XG: true})

	return i, err
}

func UpdateFileMetadata(ctx context.Context, oldFileID string) error {
	file, b, err := tgapi.FetchFileInfo(ctx, oldFileID)
	if err != nil {
		return err
	}

	newFileID := file.FileID
	if (newFileID != oldFileID) {
		log.Infof(ctx, "Detected FileID change %s -> %s", oldFileID, newFileID)
	}

	if !appengine.IsDevAppServer() {
		if err := utils.StoreFileToGCS(ctx, newFileID, b); err != nil {
			return err
		}
	}

	sum := md5.Sum(b)
	log.Infof(ctx, "File %s: %x (%d bytes)", newFileID, sum, file.FileSize)

	return nds.RunInTransaction(ctx, func(tc context.Context) error {
		i := new(Inventory)
		oldKey := inventoryKey(ctx, oldFileID)
		if err := nds.Get(ctx, oldKey, i); err != nil {
			if err == datastore.ErrNoSuchEntity {
				// Silently ignore this.
				return nil
			}
			return err
		}

		i.FileID = newFileID
		i.MD5Sum = sum[:]
		i.FileSize = file.FileSize

		if oldFileID != newFileID {
			if err := nds.Delete(ctx, oldKey); err != nil {
				return err
			}
		}
		_, err := nds.Put(ctx, inventoryKey(ctx, newFileID), i)
		return err
	}, &datastore.TransactionOptions{})
}
