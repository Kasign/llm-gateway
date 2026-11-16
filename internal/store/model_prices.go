// model_prices 保存 Provider/模型的不可变价格版本。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var decimalPricePattern = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,11})(?:\.[0-9]{1,12})?$`)

// ModelPrice 的单价单位为 USD / 1,000,000 tokens。
// 金额使用字符串承载，避免 JSON/Go float64 引入精度损失。
type ModelPrice struct {
	ID                    uint64    `json:"id"`
	ProviderID            string    `json:"provider_id"`
	ModelID               string    `json:"model_id"`
	InputPricePerMillion  string    `json:"input_price_per_million"`
	OutputPricePerMillion string    `json:"output_price_per_million"`
	Currency              string    `json:"currency"`
	EffectiveAt           time.Time `json:"effective_at"`
	CreatedAt             time.Time `json:"created_at"`
}

func validateModelPrice(p ModelPrice) error {
	p.ProviderID = strings.ToLower(strings.TrimSpace(p.ProviderID))
	p.ModelID = strings.TrimSpace(p.ModelID)
	if p.ProviderID == "" || len(p.ProviderID) > 64 {
		return errors.New("provider_id is required and must be at most 64 characters")
	}
	if p.ModelID == "" || len(p.ModelID) > 255 {
		return errors.New("model_id is required and must be at most 255 characters")
	}
	if !decimalPricePattern.MatchString(p.InputPricePerMillion) || !decimalPricePattern.MatchString(p.OutputPricePerMillion) {
		return errors.New("prices must be non-negative decimal strings with at most 12 fractional digits")
	}
	if strings.ToUpper(strings.TrimSpace(p.Currency)) != "USD" {
		return errors.New("only USD currency is supported")
	}
	if p.EffectiveAt.IsZero() {
		return errors.New("effective_at is required")
	}
	return nil
}

// CreateModelPrice 追加一个价格版本，不允许修改既有价格记录。
func (r *Repo) CreateModelPrice(ctx context.Context, p ModelPrice) (*ModelPrice, error) {
	if err := validateModelPrice(p); err != nil {
		return nil, err
	}
	p.ProviderID = strings.ToLower(strings.TrimSpace(p.ProviderID))
	p.ModelID = strings.TrimSpace(p.ModelID)
	p.Currency = "USD"
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `INSERT INTO model_prices
		(provider_id,model_id,input_price_per_million,output_price_per_million,currency,effective_at,created_at)
		VALUES(?,?,?,?,?,?,?)`,
		p.ProviderID, p.ModelID, p.InputPricePerMillion, p.OutputPricePerMillion, p.Currency, p.EffectiveAt.UTC(), now)
	if err != nil {
		return nil, fmt.Errorf("create model price version: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	p.ID = uint64(id)
	p.CreatedAt = now
	return &p, nil
}

// ListModelPrices 返回指定 Provider/模型的价格版本，按生效时间倒序。
func (r *Repo) ListModelPrices(ctx context.Context, providerID, modelID string, limit int) ([]ModelPrice, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	modelID = strings.TrimSpace(modelID)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id,provider_id,model_id,CAST(input_price_per_million AS CHAR),
		CAST(output_price_per_million AS CHAR),currency,effective_at,created_at
		FROM model_prices WHERE 1=1`
	args := make([]interface{}, 0, 3)
	if providerID != "" {
		query += " AND provider_id=?"
		args = append(args, providerID)
	}
	if modelID != "" {
		query += " AND model_id=?"
		args = append(args, modelID)
	}
	query += " ORDER BY effective_at DESC,id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ModelPrice, 0)
	for rows.Next() {
		var p ModelPrice
		if err := rows.Scan(&p.ID, &p.ProviderID, &p.ModelID, &p.InputPricePerMillion,
			&p.OutputPricePerMillion, &p.Currency, &p.EffectiveAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActiveModelPrice 获取指定请求发生时刻已经生效的最新价格版本。
func (r *Repo) ActiveModelPrice(ctx context.Context, providerID, modelID string, at time.Time) (*ModelPrice, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	modelID = strings.TrimSpace(modelID)
	if providerID == "" || modelID == "" {
		return nil, sql.ErrNoRows
	}
	var p ModelPrice
	err := r.db.QueryRowContext(ctx, `SELECT id,provider_id,model_id,CAST(input_price_per_million AS CHAR),
		CAST(output_price_per_million AS CHAR),currency,effective_at,created_at
		FROM model_prices WHERE provider_id=? AND model_id=? AND effective_at<=?
		ORDER BY effective_at DESC,id DESC LIMIT 1`, providerID, modelID, at.UTC()).Scan(
		&p.ID, &p.ProviderID, &p.ModelID, &p.InputPricePerMillion, &p.OutputPricePerMillion,
		&p.Currency, &p.EffectiveAt, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
