package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
)

// kind of a cool facility here: https://mholt.github.io/json-to-go/
type Project struct {
	Scope  string
	Owner  string
	Number string
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Items  struct {
		Nodes []ProjectItemGql `json:"nodes,omitempty"`
	} `json:"items,omitempty"`
	Fields struct {
		Nodes []struct {
			DataType string `json:"dataType,omitempty"`
			ID       string `json:"id,omitempty"`
			Name     string `json:"name,omitempty"`
			Options  []struct {
				ID   string `json:"id,omitempty"`
				Name string `json:"name,omitempty"`
			} `json:"options,omitempty"`
		} `json:"nodes,omitempty"`
	} `json:"fields,omitempty"`
}

type ProjectItemGql struct {
	Content struct {
		Closed    bool   `json:"closed,omitempty"`
		ClosedAt  any    `json:"closedAt,omitempty"`
		CreatedAt string `json:"createdAt,omitempty"`
		Number    int    `json:"number,omitempty"`
		Title     string `json:"title,omitempty"`
		URL       string `json:"url,omitempty"`
	} `json:"content,omitempty"`
	CreatedAt   string `json:"createdAt,omitempty"`
	FieldValues struct {
		Nodes []struct {
			Labels struct {
				Nodes []struct {
					Name string `json:"name,omitempty"`
				} `json:"nodes,omitempty"`
			} `json:"labels,omitempty"`
			Field struct {
				ID string `json:"id,omitempty"`
			} `json:"field,omitempty"`
			ID       string `json:"id,omitempty"`
			OptionID string `json:"optionId,omitempty"`
		} `json:"nodes,omitempty"`
	} `json:"fieldValues,omitempty"`
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

func GqlObjectForScope(scope string) string {
	switch scope {
	case "user":
		return "user"
	case "org":
		return "organization"
	default:
		return "organization"
	}
}

func NewProject(scope string, owner string, number string) *Project {
	p := new(Project)
	p.Scope = scope
	p.Owner = owner
	p.Number = number

	// todo: potentially move these to be lazy loaded
	p.UpdateItems()
	p.UpdateFields()
	return p
}

func (p *Project) UpdateItems() {
	gqlObject := GqlObjectForScope(p.Scope)
	query := loadQuery("gql/get_project_contents.gql")
	query = strings.Replace(query, "{{owner}}", gqlObject, 1)
	cmd := []string{"api", "graphql", "--paginate",
		"-F", "org=" + p.Owner,
		"-F", "number=" + p.Number,
		"-F", "first=" + "50",
		"-f", "query=" + query,
		"-q", ".data." + gqlObject + ".projectV2"}

	resp := callCLI(cmd)
	if resp == nil {
		return
	}

	if err := json.Unmarshal(resp, &p); err != nil {
		return
	}
}

func (p *Project) UpdateFields() {
	gqlObject := GqlObjectForScope(p.Scope)
	query := loadQuery("gql/get_project_fields.gql")
	query = strings.Replace(query, "{{owner}}", gqlObject, 1)
	cmd := []string{"api", "graphql", "--paginate",
		"-F", "org=" + p.Owner,
		"-F", "number=" + p.Number,
		"-F", "first=" + "50",
		"-f", "query=" + query,
		"-q", ".data." + gqlObject + ".projectV2"}

	resp := callCLI(cmd)
	if resp == nil {
		log.Fatal("failed to update fields")
		return
	}

	if err := json.Unmarshal(resp, &p); err != nil {
		log.Fatal(err)
		return
	}
}

func (p *Project) CreateField(fieldName string, fieldDataType string) error {
	cmd := []string{"project", "field-create",
		"--owner", p.Owner, p.Number,
		"--name", fieldName,
		"--data-type", fieldDataType,
		"--format", "json",
		"--jq", "\".id\""}
	response := callCLI(cmd)
	if response == nil {
		// raise error
		return fmt.Errorf("could not create field")
	}

	p.UpdateFields()
	return nil
}
func (p *Project) GetFieldId(fieldName string) (int, string) {
	for i, f := range p.Fields.Nodes {
		if f.Name == fieldName {
			return i, f.ID
		}
	}
	return -1, ""
}

type ProjectItemUpdate struct {
	FieldIndex          int
	ProjectIndex        int
	ProjectId           string
	ProjectItemId       string
	FieldId             string
	ProjectV2FieldValue string // this is an https://docs.github.com/en/graphql/reference/input-objects#projectv2fieldvalue
}

func (p *Project) UpdateCreatedAt() int {
	fieldIndex, fieldId := p.GetFieldId("Created Date")
	if fieldId == "" {
		p.CreateField("Created Date", "DATE")
		fieldIndex, fieldId = p.GetFieldId("Created Date")
	}
	updates := make([]*ProjectItemUpdate, 0, len(p.Items.Nodes))
	for itemIndex, item := range p.Items.Nodes {
		// does this item have a created date?
		hasCreatedAt := false
		for _, f := range item.FieldValues.Nodes {
			hasCreatedAt = f.Field.ID == fieldId
			if hasCreatedAt {
				break
			}
		}

		if !hasCreatedAt {
			update := new(ProjectItemUpdate)
			update.FieldId = fieldId
			update.FieldIndex = fieldIndex
			update.ProjectId = p.ID
			update.ProjectIndex = itemIndex
			update.ProjectItemId = item.ID
			update.ProjectV2FieldValue = "date:\"" + item.Content.CreatedAt + "\"" // crazy memex syntax
			updates = append(updates, update)
		}
	}

	// push updates in batches
	len_updates := len(updates)
	t := loadTemplate("gql/update_issues.tmpl")
	for i := 0; i < len_updates; i += MAX_UPDATES {
		end := i + MAX_UPDATES
		if end > len_updates {
			end = len_updates
		}

		// generate batch
		var buffer bytes.Buffer
		err := t.Execute(&buffer, updates[i:end])
		if err != nil {
			log.Fatal(err)
			continue
		}
		s := buffer.String()

		cmd := []string{"api", "graphql", "-f", "query=" + s}
		if DEBUG {
			fmt.Println("DEBUG:")
			fmt.Println("gh", strings.Join(cmd, " "))
		} else {
			// operation actually writes!
			callCLI(cmd)
		}
	}

	return len_updates
}

func (p *Project) AddIssues(issues []Issue) {
	// add issues in batches
	len_issues := len(issues)
	for i := 0; i < len_issues; i += MAX_UPDATES {
		end := i + MAX_UPDATES
		if end > len(issues) {
			end = len(issues)
		}
		// generate batch
		s := "mutation {\n"
		for _, issue := range issues[i:end] {
			s += "u" + strconv.Itoa(i) + ":addProjectV2ItemById(input:{projectId:\"" + p.ID + "\", contentId:\"" + issue.Id + "\"}) {item {id}}\n"
		}
		s += "}"
		cmd := []string{"api", "graphql", "-f", "query=" + s}
		if DEBUG {
			fmt.Println("DEBUG:")
			fmt.Println("gh", strings.Join(cmd, " "))
		} else {
			// operation actually writes!
			callCLI(cmd)
		}
	}
}
