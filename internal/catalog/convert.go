package catalog

import (
	"time"

	"github.com/whl/llm-gateway/internal/catalog/hf"
	"github.com/whl/llm-gateway/internal/catalog/ollama"
)

func listingFromHF(r hf.Row) Listing {
	dl := r.Downloads
	likes := r.Likes
	t := r.PublishedAt
	return Listing{
		Source:        SourceHuggingFace,
		ID:            r.ID,
		DisplayName:   r.DisplayName,
		CopyName:      r.ID,
		Downloads:     &dl,
		Likes:         &likes,
		PublishedAt:   &t,
		Tags:          r.Tags,
		PipelineTag:   r.PipelineTag,
		SortDownloads: dl,
		SortTime:      r.PublishedAt,
	}
}

func listingFromOllama(r ollama.Row) Listing {
	return Listing{
		Source:        SourceOllama,
		ID:            r.ID,
		DisplayName:   r.DisplayName,
		CopyName:      r.ID,
		Description:   r.Description,
		Downloads:     r.Pulls,
		UpdatedLabel:  r.UpdatedLabel,
		Tags:          r.Tags,
		ParamTags:     r.ParamTags,
		SortDownloads: r.SortPulls,
		SortTime:      time.Time{},
	}
}
