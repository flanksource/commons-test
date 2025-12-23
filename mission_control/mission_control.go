package mission_control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/commons/http"
	"github.com/google/uuid"
)

type MissionControl struct {
	HTTP      *http.Client
	ConfigDB  *http.Client
	URL       string
	Username  string
	Password  string
	Namespace string
	DB        *sql.DB
}

func (mc *MissionControl) POST(path string, body any) (*http.Response, error) {
	return mc.HTTP.R(context.TODO()).Post(path, body)
}

type Scraper struct {
	mc   *MissionControl
	Name string
	Id   string
}

type ScrapeResults struct {
	Response *http.Response
	Error    error
}

func (mc *MissionControl) GetScraper(id string) *Scraper {
	return &Scraper{
		mc:   mc,
		Id:   id,
		Name: "",
	}
}

type ScrapeResult struct {
	Errors  []string       `json:"errors"`
	Summary map[string]any `json:"scrape_summary"`
}

func (s *Scraper) Run() (*ScrapeResult, error) {
	r, err := s.mc.ConfigDB.R(context.Background()).Post("/run/"+s.Id, map[string]string{"scraper": s.Name})
	if err != nil {
		return nil, err
	}
	result := &ScrapeResult{}
	body, err := r.AsString()
	if err != nil {
		return nil, err
	}
	return result, json.Unmarshal([]byte(body), result)
}

type ResourceSelector struct {
	ID            string            `json:"id,omitempty"`
	Name          string            `json:"name,omitempty"`
	Namespace     string            `json:"namespace,omitempty"`
	Types         []string          `json:"types,omitempty"`
	Statuses      []string          `json:"statuses,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	FieldSelector string            `json:"field_selector,omitempty"`
	Search        string            `json:"search,omitempty"`
}

type SearchResourcesRequest struct {
	Limit   int                `json:"limit,omitempty"`
	Configs []ResourceSelector `json:"configs,omitempty"`
}

type SelectedResource struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Namespace  string            `json:"namespace,omitempty"`
	Type       string            `json:"type,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Config     string            `json:"config,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
}

type SearchResourcesResponse struct {
	Configs []SelectedResource `json:"configs"`
}

func (mc *MissionControl) QueryCatalog(selector ResourceSelector) ([]SelectedResource, error) {
	req := SearchResourcesRequest{
		Configs: []ResourceSelector{selector},
	}

	r, err := mc.HTTP.R(context.TODO()).Post("/resources/search", req)
	if err != nil {
		return nil, err
	}

	if !r.IsOK() {
		body, _ := r.AsString()
		return nil, fmt.Errorf("query catalog failed: %s", body)
	}

	var response SearchResourcesResponse
	if err := r.Into(&response); err != nil {
		return nil, err
	}

	return response.Configs, nil
}

func (mc *MissionControl) SearchCatalog(search string) ([]SelectedResource, error) {
	return mc.QueryCatalog(ResourceSelector{Search: search})
}

type CatalogChangesSearchRequest struct {
	CatalogID             string `query:"id" json:"id"`
	ConfigType            string `query:"config_type" json:"config_type"`
	ChangeType            string `query:"type" json:"type"`
	Severity              string `query:"severity" json:"severity"`
	IncludeDeletedConfigs bool   `query:"include_deleted_configs" json:"include_deleted_configs"`
	Depth                 int    `query:"depth" json:"depth"`
	CreatedByRaw          string `query:"created_by" json:"created_by"`
	Summary               string `query:"summary" json:"summary"`
	Source                string `query:"source" json:"source"`
	Tags                  string `query:"tags" json:"tags"`

	// To Fetch from a particular agent, provide the agent id.
	// Use `local` keyword to filter by the local agent.
	AgentID string `query:"agent_id" json:"agent_id"`

	// From date in datemath format
	From string `query:"from" json:"from"`
	// To date in datemath format
	To string `query:"to" json:"to"`

	PageSize  int    `query:"page_size" json:"page_size"`
	Page      int    `query:"page" json:"page"`
	SortBy    string `query:"sort_by" json:"sort_by"`
	sortOrder string

	// upstream | downstream | both
	Recursive string `query:"recursive" json:"recursive"`

	// FIXME: Soft toggle does not work with Recursive=both
	// In that case, soft relations are always returned
	// It also returns ALL soft relations throughout the tree
	// not just soft related to the main config item
	Soft bool `query:"soft" json:"soft"`
}

type ConfigChangeRow struct {
	AgentID           string            `gorm:"column:agent_id" json:"agent_id"`
	ExternalChangeId  string            `gorm:"column:external_change_id" json:"external_change_id"`
	ID                string            `gorm:"primaryKey;unique_index;not null;column:id" json:"id"`
	ConfigID          string            `gorm:"column:config_id;default:''" json:"config_id"`
	DeletedAt         *time.Time        `gorm:"column:deleted_at" json:"deleted_at,omitempty"`
	ChangeType        string            `gorm:"column:change_type" json:"change_type" faker:"oneof:  RunInstances, diff"`
	Severity          string            `gorm:"column:severity" json:"severity"  faker:"oneof: critical, high, medium, low, info"`
	Source            string            `gorm:"column:source" json:"source"`
	Summary           string            `gorm:"column:summary;default:null" json:"summary,omitempty"`
	CreatedAt         *time.Time        `gorm:"column:created_at" json:"created_at"`
	Count             int               `gorm:"column:count" json:"count"`
	FirstObserved     *time.Time        `gorm:"column:first_observed" json:"first_observed,omitempty"`
	ConfigName        string            `gorm:"column:name" json:"name,omitempty"`
	ConfigType        string            `gorm:"column:type" json:"type,omitempty"`
	Tags              map[string]string `gorm:"column:tags" json:"tags,omitempty"`
	CreatedBy         *uuid.UUID        `gorm:"column:created_by" json:"created_by,omitempty"`
	ExternalCreatedBy string            `gorm:"column:external_created_by" json:"external_created_by,omitempty"`
}

type CatalogChangesSearchResponse struct {
	Summary map[string]int    `json:"summary,omitempty"`
	Total   int64             `json:"total,omitempty"`
	Changes []ConfigChangeRow `json:"changes,omitempty"`
}

func (mc *MissionControl) SearchCatalogChanges(req CatalogChangesSearchRequest) (*CatalogChangesSearchResponse, error) {
	r, err := mc.HTTP.R(context.TODO()).Header("content-type", "application/json").Post("/catalog/changes", req)
	if err != nil {
		return nil, err
	}

	if !r.IsOK() {
		body, _ := r.AsString()
		return nil, fmt.Errorf("query catalog failed: %s", body)
	}

	var response CatalogChangesSearchResponse
	if err := r.Into(&response); err != nil {
		return nil, err
	}

	return &response, nil

}

func (mc *MissionControl) IsHealthy() (bool, error) {
	r, err := mc.HTTP.R(context.TODO()).Get("/health")
	if err != nil {
		return false, err
	}

	return r.IsOK(), nil
}

func (mc *MissionControl) WhoAmI() (map[string]any, bool, error) {
	r, err := mc.HTTP.R(context.TODO()).Get("/auth/whoami")
	if err != nil {
		return nil, false, err
	}

	body, err := r.AsJSON()
	return body, r.IsOK(), err
}
