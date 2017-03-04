package archiver

import (
	"io"
	"restic"
	"restic/debug"
	"restic/index"
	"time"

	"restic/errors"

	"github.com/restic/chunker"
)

// Reader allows saving a stream of data to the repository.
type Reader struct {
	restic.Repository

	Tags     []string
	Hostname string
}

// Archive reads data from the reader and saves it to the repo.
func (r *Reader) Archive(name string, rd io.Reader, p *restic.Progress) (*restic.Snapshot, restic.ID, error) {
	if name == "" {
		return nil, restic.ID{}, errors.New("no filename given")
	}

	debug.Log("start archiving %s", name)
	sn, err := restic.NewSnapshot([]string{name}, r.Tags, r.Hostname)
	if err != nil {
		return nil, restic.ID{}, err
	}

	p.Start()
	defer p.Done()

	repo := r.Repository
	chnker := chunker.New(rd, repo.Config().ChunkerPolynomial)

	debug.Log("load index")
	idx, err := index.Load(repo, nil)

	ids := restic.IDs{}
	var fileSize uint64
	cm := NewContentManager(repo.Backend(), repo.Key())

	for {
		chunk, err := chnker.Next(getBuf())
		if errors.Cause(err) == io.EOF {
			break
		}

		if err != nil {
			return nil, restic.ID{}, errors.Wrap(err, "chunker.Next()")
		}

		id := restic.Hash(chunk.Data)

		h := restic.BlobHandle{ID: id, Type: restic.DataBlob}
		if !idx.Has(h) {
			if err := cm.AddNewBlob(h, chunk.Data); err != nil {
				return nil, restic.ID{}, err
			}

			debug.Log("saved blob %v (%d bytes)\n", id.Str(), chunk.Length)
		} else {
			debug.Log("blob %v already saved in the repo\n", id.Str())
		}

		freeBuf(chunk.Data)

		ids = append(ids, id)

		p.Report(restic.Stat{Bytes: uint64(chunk.Length)})
		fileSize += uint64(chunk.Length)

		if err = cm.SaveFullFile(); err != nil {
			return nil, restic.ID{}, err
		}
	}

	if err = cm.SaveAllFiles(); err != nil {
		return nil, restic.ID{}, err
	}

	tree := &restic.Tree{
		Nodes: []*restic.Node{
			&restic.Node{
				Name:       name,
				AccessTime: time.Now(),
				ModTime:    time.Now(),
				Type:       "file",
				Mode:       0644,
				Size:       fileSize,
				UID:        sn.UID,
				GID:        sn.GID,
				User:       sn.Username,
				Content:    ids,
			},
		},
	}

	treeID, err := repo.SaveTree(tree)
	if err != nil {
		return nil, restic.ID{}, err
	}
	sn.Tree = &treeID
	debug.Log("tree saved as %v", treeID.Str())

	// save new index
	id, err := index.Save(repo, cm.Packs, nil)
	if err != nil {
		return nil, restic.ID{}, err
	}
	debug.Log("new index saved as %v", id.Str())

	id, err = repo.SaveJSONUnpacked(restic.SnapshotFile, sn)
	if err != nil {
		return nil, restic.ID{}, err
	}

	debug.Log("snapshot saved as %v", id.Str())

	err = repo.Flush()
	if err != nil {
		return nil, restic.ID{}, err
	}

	err = repo.SaveIndex()
	if err != nil {
		return nil, restic.ID{}, err
	}

	return sn, id, nil
}
