package opensearch

// TemplateName is the OpenSearch index template name.
const TemplateName = "dms-documents-template"

// TemplateJSON is the full index template applied on startup. Includes
// mapping (strict), analysis (autocomplete edge-ngram), and shard config.
const TemplateJSON = `{
  "index_patterns": ["dms-documents-*"],
  "template": {
    "mappings": {
      "dynamic": "strict",
      "properties": {
        "tenant_id":       {"type": "keyword"},
        "document_id":     {"type": "keyword"},
        "workspace_id":    {"type": "keyword"},
        "folder_id":       {"type": "keyword"},
        "folder_path":     {"type": "keyword"},
        "title": {
          "type": "text",
          "analyzer": "standard",
          "fields": {
            "keyword":      {"type": "keyword", "ignore_above": 256},
            "autocomplete": {"type": "text", "analyzer": "autocomplete_analyzer", "search_analyzer": "standard"}
          }
        },
        "description":     {"type": "text"},
        "content":         {"type": "text", "analyzer": "standard"},
        "content_snippet": {"type": "text", "index": false},
        "tags":            {"type": "keyword"},
        "document_class":  {"type": "keyword"},
        "lifecycle_state": {"type": "keyword"},
        "region_pin":      {"type": "keyword"},
        "mime_type":       {"type": "keyword"},
        "size_bytes":      {"type": "long"},
        "created_by":      {"type": "keyword"},
        "created_by_name": {"type": "keyword"},
        "created_at":      {"type": "date"},
        "updated_at":      {"type": "date"},
        "custom_metadata": {"type": "object", "dynamic": true},
        "readable_by":     {"type": "keyword"},
        "has_thumbnail":   {"type": "boolean"},
        "version_count":   {"type": "integer"},
        "extracted_entities": {
          "type": "object",
          "properties": {
            "people":        {"type": "keyword"},
            "organizations": {"type": "keyword"},
            "locations":     {"type": "keyword"},
            "dates":         {"type": "date", "format": "yyyy-MM-dd||yyyy-MM||yyyy"},
            "amounts":       {"type": "keyword"}
          }
        }
      }
    },
    "settings": {
      "number_of_shards": 3,
      "number_of_replicas": 1,
      "analysis": {
        "analyzer": {
          "autocomplete_analyzer": {
            "type": "custom",
            "tokenizer": "standard",
            "filter": ["lowercase", "autocomplete_filter"]
          }
        },
        "filter": {
          "autocomplete_filter": {
            "type": "edge_ngram",
            "min_gram": 2,
            "max_gram": 20
          }
        }
      },
      "index.mapping.total_fields.limit": 500
    }
  },
  "priority": 200
}`
