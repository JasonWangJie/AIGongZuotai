package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

const storageOwnerName = ".studio-storage-owner.json"

type storageOwner struct {
	Version int    `json:"version"`
	ID      string `json:"storage_id"`
	Role    string `json:"role"`
}

func storageOwnerID(ctx context.Context, conn *pgx.Conn) (string, error) {
	var id string
	if err := conn.QueryRow(ctx, "SELECT storage_id::text FROM studio_storage_identity WHERE singleton=true").Scan(&id); err != nil || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(id) {
		return "", errors.New("private storage identity is missing or invalid; restore the matching database")
	}
	return id, nil
}

func readStorageOwner(root *os.Root) (storageOwner, error) {
	var owner storageOwner
	info, err := root.Lstat(storageOwnerName)
	if err != nil {
		return owner, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return owner, errors.New("private storage owner marker is unsafe")
	}
	f, err := root.Open(storageOwnerName)
	if err != nil {
		return owner, err
	}
	defer f.Close()
	if err = json.NewDecoder(io.LimitReader(f, 1025)).Decode(&owner); err != nil || owner.Version != 1 || owner.ID == "" || (owner.Role != "references" && owner.Role != "assets") {
		return owner, errors.New("private storage owner marker is invalid")
	}
	return owner, nil
}

// CheckMemoryStorageOwnership prevents an unauthenticated ephemeral instance
// from adding files into an already-bound persistent database's private tree.
func CheckMemoryStorageOwnership(directories ...string) error {
	for _, directory := range directories {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return errors.New("private storage directory unavailable")
		}
		_, err = readStorageOwner(root)
		root.Close()
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("private storage belongs to PostgreSQL; configure its database or use separate empty directories")
		}
	}
	return nil
}

// BindStorageOwnership must run under the directory leases and DB writer lock.
// Legacy directories are adopted only if every published object is explained
// by this database. It never treats an empty, unrelated database as authority.
func BindStorageOwnership(ctx context.Context, conn *pgx.Conn, referenceDir, assetDir string) error {
	id, err := storageOwnerID(ctx, conn)
	if err != nil {
		return err
	}
	roots := make([]*os.Root, 0, 2)
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	roles := []string{"references", "assets"}
	missing := []bool{false, false}
	for index, directory := range []string{referenceDir, assetDir} {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return errors.New("private storage directory unavailable")
		}
		roots = append(roots, root)
		owner, err := readStorageOwner(root)
		if errors.Is(err, os.ErrNotExist) {
			missing[index] = true
			continue
		}
		if err != nil || owner.ID != id || owner.Role != roles[index] {
			return errors.New("private storage owner does not match this database and role; use its matching backup")
		}
	}
	if !missing[0] && !missing[1] {
		return nil
	}
	refs, assets, err := maintenanceReachable(ctx, conn)
	if err != nil {
		return err
	}
	for index, root := range roots {
		if !missing[index] {
			continue
		}
		entries, err := root.Open(".")
		if err != nil {
			return errors.New("legacy private storage cannot be inspected")
		}
		seen := 0
		for {
			batch, readErr := entries.ReadDir(256)
			for _, entry := range batch {
				seen++
				if seen > maintenanceEntryLimit {
					entries.Close()
					return errors.New("legacy storage adoption exceeds scan budget")
				}
				name := entry.Name()
				if name == storageLeaseName {
					continue
				}
				info, statErr := entry.Info()
				if statErr != nil || !info.Mode().IsRegular() {
					entries.Close()
					return errors.New("legacy storage contains unrecognized files; retain backup and resolve ownership before adoption")
				}
				owned := refs[name]
				if index == 1 {
					owned = len(name) >= 70 && assets[name[:70]] && assetGCFile.MatchString(name)
				}
				// Unpublished temporary files do not belong to durable user assets.
				temporary := (index == 0 && referenceGCFile.MatchString(name) || index == 1 && assetGCFile.MatchString(name)) && strings.HasSuffix(name, ".tmp")
				if !owned && !temporary {
					entries.Close()
					return errors.New("legacy storage contains files not owned by this database; restore matching metadata before adoption")
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				entries.Close()
				return errors.New("legacy storage scan failed")
			}
		}
		entries.Close()
	}
	// All checks precede marker creation. A partial marker write refuses further
	// startup; it is never silently replaced with a different database identity.
	for index, root := range roots {
		if !missing[index] {
			continue
		}
		f, err := root.OpenFile(storageOwnerName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return errors.New("private storage owner marker cannot be created")
		}
		writeErr := json.NewEncoder(f).Encode(storageOwner{Version: 1, ID: id, Role: roles[index]})
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return errors.New("private storage owner marker persistence failed; inspect it before restart")
		}
	}
	return nil
}

func requireStorageOwnership(ctx context.Context, conn *pgx.Conn, referenceRoot, assetRoot *os.Root) error {
	id, err := storageOwnerID(ctx, conn)
	if err != nil {
		return err
	}
	for index, root := range []*os.Root{referenceRoot, assetRoot} {
		owner, err := readStorageOwner(root)
		role := []string{"references", "assets"}[index]
		if err != nil || owner.ID != id || owner.Role != role {
			return errors.New("maintenance directory owner differs from the database; no files are eligible for removal")
		}
	}
	return nil
}
