package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"email-invoice-guard/internal/core"
)

// SaveAsset upserts an asset by its explicit identity (hostname or product ID)
// scoped to provider and account. Assets with no explicit identity are stored
// unassociated and never merged. Known service periods only widen.
func (r *repo) SaveAsset(ctx context.Context, a core.Asset) (core.Asset, error) {
	var out core.Asset
	err := r.inTx(ctx, func(t *repo) error {
		if a.ProviderID == "" || a.AccountID == "" {
			return fmt.Errorf("%w: asset provider/account", core.ErrInvalid)
		}
		now := time.Now().UTC()
		if a.ID == "" {
			a.ID = core.NewID()
		}
		if a.CreatedAt.IsZero() {
			a.CreatedAt = now
		}
		a.UpdatedAt = now

		existingID, err := t.findAsset(ctx, a)
		if err != nil {
			return err
		}
		if existingID != "" {
			cur, err := t.getAsset(ctx, existingID)
			if err != nil {
				return err
			}
			mergeAsset(&cur, a)
			cur.UpdatedAt = now
			js, err := marshalJSON(cur)
			if err != nil {
				return err
			}
			if _, err := t.q.ExecContext(ctx, `UPDATE assets
				SET hostname = ?, product_id = ?, asset_json = ?, last_invoice_id = ?, updated_at = ?
				WHERE id = ?`,
				nullStringPtr(cur.Hostname), nullStringPtr(cur.ProductID), js,
				nullStringPtr(cur.LastInvoiceID), fmtTime(now), cur.ID); err != nil {
				return mapErr(err)
			}
			out = cur
			return nil
		}

		js, err := marshalJSON(a)
		if err != nil {
			return err
		}
		if _, err := t.q.ExecContext(ctx, `INSERT INTO assets
			(id, provider_id, account_id, hostname, product_id, asset_json, last_invoice_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.ProviderID, a.AccountID, nullStringPtr(a.Hostname), nullStringPtr(a.ProductID),
			js, nullStringPtr(a.LastInvoiceID), fmtTime(a.CreatedAt), fmtTime(a.UpdatedAt)); err != nil {
			return mapErr(err)
		}
		out = a
		return nil
	})
	return out, err
}

func (r *repo) findAsset(ctx context.Context, a core.Asset) (string, error) {
	var id string
	var err error
	if a.Hostname != nil && *a.Hostname != "" {
		err = r.q.QueryRowContext(ctx, `SELECT id FROM assets WHERE provider_id = ? AND account_id = ? AND hostname = ?`,
			a.ProviderID, a.AccountID, *a.Hostname).Scan(&id)
	} else if a.ProductID != nil && *a.ProductID != "" {
		err = r.q.QueryRowContext(ctx, `SELECT id FROM assets WHERE provider_id = ? AND account_id = ? AND product_id = ?`,
			a.ProviderID, a.AccountID, *a.ProductID).Scan(&id)
	} else {
		return "", nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (r *repo) getAsset(ctx context.Context, id string) (core.Asset, error) {
	var a core.Asset
	var hostname, productID, lastInvoice, js, createdAt, updatedAt sql.NullString
	var providerID, accountID string
	err := r.q.QueryRowContext(ctx, `SELECT id, provider_id, account_id, hostname, product_id, asset_json, last_invoice_id, created_at, updated_at
		FROM assets WHERE id = ?`, id).
		Scan(&a.ID, &providerID, &accountID, &hostname, &productID, &js, &lastInvoice, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Asset{}, core.ErrNotFound
	}
	if err != nil {
		return core.Asset{}, err
	}
	if js.Valid {
		if err := json.Unmarshal([]byte(js.String), &a); err != nil {
			return core.Asset{}, fmt.Errorf("decode asset: %w", err)
		}
	}
	a.ProviderID = providerID
	a.AccountID = accountID
	a.Hostname = stringPtr(hostname)
	a.ProductID = stringPtr(productID)
	a.LastInvoiceID = stringPtr(lastInvoice)
	a.CreatedAt = parseTime(createdAt.String)
	a.UpdatedAt = parseTime(updatedAt.String)
	return a, nil
}

// mergeAsset widens known service periods and fills descriptive fields without
// discarding newer information.
func mergeAsset(cur *core.Asset, next core.Asset) {
	if next.ProductModel != nil {
		cur.ProductModel = next.ProductModel
	}
	if next.OperatingSystem != nil {
		cur.OperatingSystem = next.OperatingSystem
	}
	if next.Hostname != nil {
		cur.Hostname = next.Hostname
	}
	if next.ProductID != nil {
		cur.ProductID = next.ProductID
	}
	if next.LastKnownServiceStart != nil && (cur.LastKnownServiceStart == nil || *next.LastKnownServiceStart < *cur.LastKnownServiceStart) {
		cur.LastKnownServiceStart = next.LastKnownServiceStart
	}
	if next.LastKnownServiceEnd != nil && (cur.LastKnownServiceEnd == nil || *next.LastKnownServiceEnd > *cur.LastKnownServiceEnd) {
		cur.LastKnownServiceEnd = next.LastKnownServiceEnd
	}
	if next.LastInvoiceID != nil {
		cur.LastInvoiceID = next.LastInvoiceID
	}
}

// ListAssets filters by provider and account.
func (r *repo) ListAssets(ctx context.Context, filter core.AssetFilter) ([]core.Asset, error) {
	query := `SELECT id FROM assets WHERE 1 = 1`
	var args []any
	if filter.ProviderID != "" {
		query += ` AND provider_id = ?`
		args = append(args, filter.ProviderID)
	}
	if filter.AccountID != "" {
		query += ` AND account_id = ?`
		args = append(args, filter.AccountID)
	}
	query += ` ORDER BY updated_at DESC, id`
	// Limit <= 0 is an internal full-scan request (for example the asset
	// linker); a positive limit is capped to a safe maximum.
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, capPositiveLimit(filter.Limit))
	}
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]core.Asset, 0, len(ids))
	for _, id := range ids {
		a, err := r.getAsset(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// LinkAssetInvoice records the association and updates the asset's last
// invoice pointer.
func (r *repo) LinkAssetInvoice(ctx context.Context, assetID, invoiceID string) error {
	return r.inTx(ctx, func(t *repo) error {
		if _, err := t.q.ExecContext(ctx, `INSERT OR IGNORE INTO asset_invoice_links (asset_id, invoice_id) VALUES (?, ?)`,
			assetID, invoiceID); err != nil {
			return mapErr(err)
		}
		res, err := t.q.ExecContext(ctx, `UPDATE assets SET last_invoice_id = ?, updated_at = ? WHERE id = ?`,
			invoiceID, fmtTime(time.Now()), assetID)
		if err != nil {
			return mapErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return core.ErrNotFound
		}
		return nil
	})
}

// AssetInvoices returns the invoices linked to an asset.
func (r *repo) AssetInvoices(ctx context.Context, assetID string) ([]core.Invoice, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+invoiceColumns+` FROM invoices
		WHERE id IN (SELECT invoice_id FROM asset_invoice_links WHERE asset_id = ?)
		ORDER BY created_at DESC, id`, assetID)
	if err != nil {
		return nil, err
	}
	var out []core.Invoice
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		items, err := r.invoiceItems(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

func stringPtr(n sql.NullString) *string {
	if !n.Valid || n.String == "" {
		return nil
	}
	v := n.String
	return &v
}
