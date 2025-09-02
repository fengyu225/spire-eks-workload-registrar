package spireapi

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	"github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Entry represents a SPIRE registration entry
type Entry struct {
	ID            string
	SPIFFEID      spiffeid.ID
	ParentID      spiffeid.ID
	Selectors     []Selector
	X509SVIDTTL   int32
	JWTSVIDTTL    int32
	FederatesWith []spiffeid.TrustDomain
	Admin         bool
	Downstream    bool
	DNSNames      []string
	Hint          string
}

// Selector represents a workload selector
type Selector struct {
	Type  string
	Value string
}

// Status represents a SPIRE API operation status
type Status struct {
	Code    codes.Code
	Message string
}

func (s Status) Err() error {
	return status.New(s.Code, s.Message).Err()
}

func statusFromAPI(in *types.Status) Status {
	if in == nil {
		return Status{
			Code:    codes.Unknown,
			Message: "status is nil",
		}
	}
	return Status{
		Code:    codes.Code(in.Code),
		Message: in.Message,
	}
}

type Client interface {
	ListEntries(ctx context.Context) ([]Entry, error)
	CreateEntry(ctx context.Context, entry Entry) error
	UpdateEntry(ctx context.Context, entry Entry) error
	DeleteEntry(ctx context.Context, id string) error
	BatchCreateEntries(ctx context.Context, entries []Entry) ([]Status, error)
	BatchUpdateEntries(ctx context.Context, entries []Entry) ([]Status, error)
	BatchDeleteEntries(ctx context.Context, ids []string) ([]Status, error)
	Close() error
}

type client struct {
	conn  *grpc.ClientConn
	entry entryv1.EntryClient
}

// NewClient creates a client that connects via Unix socket or TCP address
func NewClient(target string) (Client, error) {
	// If the target looks like a file path, treat it as a Unix socket
	if filepath.IsAbs(target) || target[0] == '.' || target[0] == '/' {
		if filepath.IsAbs(target) {
			target = "unix://" + target
		} else {
			target = "unix:" + target
		}
	}

	conn, err := grpc.Dial(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to dial SPIRE server: %w", err)
	}

	return &client{
		conn:  conn,
		entry: entryv1.NewEntryClient(conn),
	}, nil
}

// Batch operation constants
var (
	entryCreateBatchSize = 50
	entryUpdateBatchSize = 50
	entryDeleteBatchSize = 200
	entryListPageSize    = 200
)

// runBatch runs a function in batches
func runBatch(size, batch int, fn func(start, end int) error) error {
	if batch < 1 {
		batch = size
	}
	for i := 0; i < size; {
		n := size - i
		if n > batch {
			n = batch
		}
		err := fn(i, i+n)
		if err != nil {
			return err
		}
		i += n
	}
	return nil
}

func (c *client) Close() error {
	return c.conn.Close()
}

func (c *client) ListEntries(ctx context.Context) ([]Entry, error) {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(2).Info("Starting to list SPIRE entries")

	var entries []Entry
	pageToken := ""
	totalPages := 0

	for {
		totalPages++
		logger.V(3).Info("Fetching entries page", "page", totalPages, "pageToken", pageToken)

		resp, err := c.entry.ListEntries(ctx, &entryv1.ListEntriesRequest{
			PageToken: pageToken,
			PageSize:  1000,
		})
		if err != nil {
			logger.Error(err, "Failed to list entries from SPIRE server", "page", totalPages)
			return nil, fmt.Errorf("failed to list entries: %w", err)
		}

		pageEntries := 0
		for _, e := range resp.Entries {
			entry, err := fromAPIEntry(e)
			if err != nil {
				logger.Error(err, "Failed to convert API entry", "entryID", e.Id)
				return nil, err
			}
			entries = append(entries, *entry)
			pageEntries++
		}

		logger.V(3).Info("Fetched entries from page", "page", totalPages, "entriesInPage", pageEntries)

		pageToken = resp.NextPageToken
		if pageToken == "" {
			break
		}
	}

	logger.V(1).Info("Successfully listed SPIRE entries", "totalEntries", len(entries), "totalPages", totalPages)
	return entries, nil
}

func (c *client) CreateEntry(ctx context.Context, entry Entry) error {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(2).Info("Creating SPIRE entry",
		"spiffeID", entry.SPIFFEID.String(),
		"parentID", entry.ParentID.String(),
		"hint", entry.Hint,
		"selectorsCount", len(entry.Selectors))

	apiEntry := toAPIEntry(entry)

	resp, err := c.entry.BatchCreateEntry(ctx, &entryv1.BatchCreateEntryRequest{
		Entries: []*types.Entry{apiEntry},
	})
	if err != nil {
		logger.Error(err, "Failed to create entry via SPIRE API",
			"spiffeID", entry.SPIFFEID.String(),
			"hint", entry.Hint)
		return fmt.Errorf("failed to create entry: %w", err)
	}

	if len(resp.Results) > 0 && resp.Results[0].Status.Code != 0 {
		// Status code 6 means "similar entry already exists" - treat as success
		if resp.Results[0].Status.Code == 6 {
			logger.V(1).Info("Entry already exists, treating as success",
				"spiffeID", entry.SPIFFEID.String(),
				"statusMessage", resp.Results[0].Status.Message,
				"hint", entry.Hint)
			return nil
		}

		logger.Error(nil, "SPIRE server returned error for entry creation",
			"spiffeID", entry.SPIFFEID.String(),
			"statusCode", resp.Results[0].Status.Code,
			"statusMessage", resp.Results[0].Status.Message,
			"hint", entry.Hint)
		return fmt.Errorf("failed to create entry: %s", resp.Results[0].Status.Message)
	}

	// Get the created entry ID from response
	var createdID string
	if len(resp.Results) > 0 && resp.Results[0].Entry != nil {
		createdID = resp.Results[0].Entry.Id
	}

	logger.V(1).Info("Successfully created SPIRE entry",
		"spiffeID", entry.SPIFFEID.String(),
		"entryID", createdID,
		"hint", entry.Hint)

	return nil
}

func (c *client) UpdateEntry(ctx context.Context, entry Entry) error {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(2).Info("Updating SPIRE entry",
		"entryID", entry.ID,
		"spiffeID", entry.SPIFFEID.String(),
		"parentID", entry.ParentID.String(),
		"hint", entry.Hint,
		"selectorsCount", len(entry.Selectors))

	apiEntry := toAPIEntry(entry)

	resp, err := c.entry.BatchUpdateEntry(ctx, &entryv1.BatchUpdateEntryRequest{
		Entries: []*types.Entry{apiEntry},
	})
	if err != nil {
		logger.Error(err, "Failed to update entry via SPIRE API",
			"entryID", entry.ID,
			"spiffeID", entry.SPIFFEID.String(),
			"hint", entry.Hint)
		return fmt.Errorf("failed to update entry: %w", err)
	}

	if len(resp.Results) > 0 && resp.Results[0].Status.Code != 0 {
		logger.Error(nil, "SPIRE server returned error for entry update",
			"entryID", entry.ID,
			"spiffeID", entry.SPIFFEID.String(),
			"statusCode", resp.Results[0].Status.Code,
			"statusMessage", resp.Results[0].Status.Message,
			"hint", entry.Hint)
		return fmt.Errorf("failed to update entry: %s", resp.Results[0].Status.Message)
	}

	logger.V(1).Info("Successfully updated SPIRE entry",
		"entryID", entry.ID,
		"spiffeID", entry.SPIFFEID.String(),
		"hint", entry.Hint)

	return nil
}

func (c *client) DeleteEntry(ctx context.Context, id string) error {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(2).Info("Deleting SPIRE entry", "entryID", id)

	resp, err := c.entry.BatchDeleteEntry(ctx, &entryv1.BatchDeleteEntryRequest{
		Ids: []string{id},
	})
	if err != nil {
		logger.Error(err, "Failed to delete entry via SPIRE API", "entryID", id)
		return fmt.Errorf("failed to delete entry: %w", err)
	}

	if len(resp.Results) > 0 && resp.Results[0].Status.Code != 0 {
		logger.Error(nil, "SPIRE server returned error for entry deletion",
			"entryID", id,
			"statusCode", resp.Results[0].Status.Code,
			"statusMessage", resp.Results[0].Status.Message)
		return fmt.Errorf("failed to delete entry: %s", resp.Results[0].Status.Message)
	}

	logger.V(1).Info("Successfully deleted SPIRE entry", "entryID", id)
	return nil
}

// BatchCreateEntries creates multiple entries in batches
func (c *client) BatchCreateEntries(ctx context.Context, entries []Entry) ([]Status, error) {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(1).Info("Starting batch create entries", "totalEntries", len(entries), "batchSize", entryCreateBatchSize)

	statuses := make([]Status, 0, len(entries))
	batchNum := 0
	successCount := 0
	errorCount := 0

	err := runBatch(len(entries), entryCreateBatchSize, func(start, end int) error {
		batchNum++
		batchEntries := entries[start:end]
		logger.V(2).Info("Processing create batch", "batch", batchNum, "entriesInBatch", len(batchEntries), "startIndex", start, "endIndex", end)

		resp, err := c.entry.BatchCreateEntry(ctx, &entryv1.BatchCreateEntryRequest{
			Entries: entriesToAPI(batchEntries),
		})
		if err != nil {
			logger.Error(err, "Failed to create batch via SPIRE API", "batch", batchNum, "entriesInBatch", len(batchEntries))
			return err
		}

		batchSuccessCount := 0
		batchErrorCount := 0
		for i, result := range resp.Results {
			status := statusFromAPI(result.Status)
			statuses = append(statuses, status)

			if status.Code == codes.OK {
				batchSuccessCount++
				successCount++
				if result.Entry != nil {
					logger.V(3).Info("Successfully created entry in batch",
						"batch", batchNum,
						"entryIndex", i,
						"entryID", result.Entry.Id,
						"spiffeID", batchEntries[i].SPIFFEID.String())
				}
			} else {
				batchErrorCount++
				errorCount++
				logger.Error(nil, "Failed to create entry in batch",
					"batch", batchNum,
					"entryIndex", i,
					"spiffeID", batchEntries[i].SPIFFEID.String(),
					"statusCode", status.Code,
					"statusMessage", status.Message)
			}
		}

		logger.V(2).Info("Completed create batch",
			"batch", batchNum,
			"entriesInBatch", len(batchEntries),
			"successCount", batchSuccessCount,
			"errorCount", batchErrorCount)

		return nil
	})

	logger.V(1).Info("Completed batch create entries",
		"totalEntries", len(entries),
		"totalBatches", batchNum,
		"successCount", successCount,
		"errorCount", errorCount)

	return statuses, err
}

// BatchUpdateEntries updates multiple entries in batches
func (c *client) BatchUpdateEntries(ctx context.Context, entries []Entry) ([]Status, error) {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(1).Info("Starting batch update entries", "totalEntries", len(entries), "batchSize", entryUpdateBatchSize)

	statuses := make([]Status, 0, len(entries))
	batchNum := 0
	successCount := 0
	errorCount := 0

	err := runBatch(len(entries), entryUpdateBatchSize, func(start, end int) error {
		batchNum++
		batchEntries := entries[start:end]
		logger.V(2).Info("Processing update batch", "batch", batchNum, "entriesInBatch", len(batchEntries), "startIndex", start, "endIndex", end)

		resp, err := c.entry.BatchUpdateEntry(ctx, &entryv1.BatchUpdateEntryRequest{
			Entries: entriesToAPI(batchEntries),
		})
		if err != nil {
			logger.Error(err, "Failed to update batch via SPIRE API", "batch", batchNum, "entriesInBatch", len(batchEntries))
			return err
		}

		batchSuccessCount := 0
		batchErrorCount := 0
		for i, result := range resp.Results {
			status := statusFromAPI(result.Status)
			statuses = append(statuses, status)

			if status.Code == codes.OK {
				batchSuccessCount++
				successCount++
				logger.V(3).Info("Successfully updated entry in batch",
					"batch", batchNum,
					"entryIndex", i,
					"entryID", batchEntries[i].ID,
					"spiffeID", batchEntries[i].SPIFFEID.String())
			} else {
				batchErrorCount++
				errorCount++
				logger.Error(nil, "Failed to update entry in batch",
					"batch", batchNum,
					"entryIndex", i,
					"entryID", batchEntries[i].ID,
					"spiffeID", batchEntries[i].SPIFFEID.String(),
					"statusCode", status.Code,
					"statusMessage", status.Message)
			}
		}

		logger.V(2).Info("Completed update batch",
			"batch", batchNum,
			"entriesInBatch", len(batchEntries),
			"successCount", batchSuccessCount,
			"errorCount", batchErrorCount)

		return nil
	})

	logger.V(1).Info("Completed batch update entries",
		"totalEntries", len(entries),
		"totalBatches", batchNum,
		"successCount", successCount,
		"errorCount", errorCount)

	return statuses, err
}

// BatchDeleteEntries deletes multiple entries in batches
func (c *client) BatchDeleteEntries(ctx context.Context, ids []string) ([]Status, error) {
	logger := log.FromContext(ctx).WithName("spire-client")
	logger.V(1).Info("Starting batch delete entries", "totalEntries", len(ids), "batchSize", entryDeleteBatchSize)

	statuses := make([]Status, 0, len(ids))
	batchNum := 0
	successCount := 0
	errorCount := 0

	err := runBatch(len(ids), entryDeleteBatchSize, func(start, end int) error {
		batchNum++
		batchIDs := ids[start:end]
		logger.V(2).Info("Processing delete batch", "batch", batchNum, "entriesInBatch", len(batchIDs), "startIndex", start, "endIndex", end)

		resp, err := c.entry.BatchDeleteEntry(ctx, &entryv1.BatchDeleteEntryRequest{
			Ids: batchIDs,
		})
		if err != nil {
			logger.Error(err, "Failed to delete batch via SPIRE API", "batch", batchNum, "entriesInBatch", len(batchIDs))
			return err
		}

		batchSuccessCount := 0
		batchErrorCount := 0
		for i, result := range resp.Results {
			status := statusFromAPI(result.Status)
			statuses = append(statuses, status)

			if status.Code == codes.OK {
				batchSuccessCount++
				successCount++
				logger.V(3).Info("Successfully deleted entry in batch",
					"batch", batchNum,
					"entryIndex", i,
					"entryID", batchIDs[i])
			} else {
				batchErrorCount++
				errorCount++
				logger.Error(nil, "Failed to delete entry in batch",
					"batch", batchNum,
					"entryIndex", i,
					"entryID", batchIDs[i],
					"statusCode", status.Code,
					"statusMessage", status.Message)
			}
		}

		logger.V(2).Info("Completed delete batch",
			"batch", batchNum,
			"entriesInBatch", len(batchIDs),
			"successCount", batchSuccessCount,
			"errorCount", batchErrorCount)

		return nil
	})

	logger.V(1).Info("Completed batch delete entries",
		"totalEntries", len(ids),
		"totalBatches", batchNum,
		"successCount", successCount,
		"errorCount", errorCount)

	return statuses, err
}

// entriesToAPI converts Entry slice to API types
func entriesToAPI(entries []Entry) []*types.Entry {
	apiEntries := make([]*types.Entry, len(entries))
	for i, entry := range entries {
		apiEntries[i] = toAPIEntry(entry)
	}
	return apiEntries
}

func toAPIEntry(entry Entry) *types.Entry {
	selectors := make([]*types.Selector, len(entry.Selectors))
	for i, sel := range entry.Selectors {
		selectors[i] = &types.Selector{
			Type:  sel.Type,
			Value: sel.Value,
		}
	}

	federatesWith := make([]string, len(entry.FederatesWith))
	for i, td := range entry.FederatesWith {
		federatesWith[i] = td.String()
	}

	return &types.Entry{
		Id: entry.ID,
		SpiffeId: &types.SPIFFEID{
			TrustDomain: entry.SPIFFEID.TrustDomain().String(),
			Path:        entry.SPIFFEID.Path(),
		},
		ParentId: &types.SPIFFEID{
			TrustDomain: entry.ParentID.TrustDomain().String(),
			Path:        entry.ParentID.Path(),
		},
		Selectors:     selectors,
		X509SvidTtl:   entry.X509SVIDTTL,
		JwtSvidTtl:    entry.JWTSVIDTTL,
		FederatesWith: federatesWith,
		Admin:         entry.Admin,
		Downstream:    entry.Downstream,
		DnsNames:      entry.DNSNames,
		Hint:          entry.Hint,
	}
}

func fromAPIEntry(e *types.Entry) (*Entry, error) {
	spiffeID, err := spiffeid.FromString(fmt.Sprintf("spiffe://%s%s",
		e.SpiffeId.TrustDomain, e.SpiffeId.Path))
	if err != nil {
		return nil, err
	}

	parentID, err := spiffeid.FromString(fmt.Sprintf("spiffe://%s%s",
		e.ParentId.TrustDomain, e.ParentId.Path))
	if err != nil {
		return nil, err
	}

	selectors := make([]Selector, len(e.Selectors))
	for i, sel := range e.Selectors {
		selectors[i] = Selector{
			Type:  sel.Type,
			Value: sel.Value,
		}
	}

	federatesWith := make([]spiffeid.TrustDomain, len(e.FederatesWith))
	for i, td := range e.FederatesWith {
		trustDomain, err := spiffeid.TrustDomainFromString(td)
		if err != nil {
			return nil, err
		}
		federatesWith[i] = trustDomain
	}

	return &Entry{
		ID:            e.Id,
		SPIFFEID:      spiffeID,
		ParentID:      parentID,
		Selectors:     selectors,
		X509SVIDTTL:   e.X509SvidTtl,
		JWTSVIDTTL:    e.JwtSvidTtl,
		FederatesWith: federatesWith,
		Admin:         e.Admin,
		Downstream:    e.Downstream,
		DNSNames:      e.DnsNames,
		Hint:          e.Hint,
	}, nil
}
