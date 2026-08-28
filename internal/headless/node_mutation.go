// Node creation/removal: add_node and remove_node.
//
// Both operate through scripts/godot_operations.gd exactly like
// SetNodeProperty (see its doc comment): load the scene, instantiate it,
// mutate the live node tree, then re-pack and save — producing a correct
// .tscn byte stream is Godot's own serializer's job, not something these
// operations reconstruct by hand. Both also go through the same
// re-pack-and-save round trip that drops resource uid="..." attributes, so
// both restore them afterward via preserveSceneUIDs, same as
// SetNodeProperty.
package headless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AddNodeParams are the parameters for the add_node operation. Exactly one
// of TypeName/InstanceScenePath must be set: TypeName creates a bare node of
// a built-in Godot engine class (e.g. "Sprite2D"), while InstanceScenePath
// instances another project .tscn as a child (composing a prefab/sub-scene),
// matching how the editor's "Instance Child Scene" works — the sub-scene is
// kept as a live reference (an ext_resource "instance" node), not flattened
// into the parent scene's own node list.
//
// TypeName is restricted to ClassDB-registered built-in engine classes, not
// a project-defined class_name type: a class_name type is script-backed, so
// instantiating one always attaches that script to the new node — a
// materially different trust question from creating a plain engine node,
// deliberately deferred rather than folded in here (see FEATURES.md).
type AddNodeParams struct {
	// ScenePath is the .tscn path, relative to the project root. Validated
	// against Root before use. The scene must already exist — this
	// operation never creates a new scene file.
	ScenePath string `json:"scene_path" jsonschema:"path to an existing .tscn scene file, relative to the project root"`
	// ParentNodePath addresses the new node's parent, relative to the scene
	// root, using Godot's own NodePath syntax (e.g. "World"). Empty string
	// means the scene root itself.
	ParentNodePath string `json:"parent_node_path" jsonschema:"the new node's parent, relative to the scene root, using Godot's own NodePath syntax (e.g. \"World\"); empty string means the scene root itself"`
	// Name is the new node's name. Rejected outright if a sibling with this
	// name already exists under the parent — never silently renamed.
	Name string `json:"name" jsonschema:"the new node's name; rejected if a sibling with this name already exists under parent_node_path"`

	// TypeName creates a bare node of this built-in Godot engine class
	// (e.g. "Sprite2D", "CollisionShape2D"). Must be a Node subclass.
	// Exactly one of TypeName/InstanceScenePath must be set.
	TypeName *string `json:"type_name,omitempty" jsonschema:"a built-in Godot engine class name that is a Node subclass, e.g. \"Sprite2D\"; exactly one of type_name/instance_scene_path must be set"`
	// InstanceScenePath instances another .tscn file, relative to the
	// project root, as the new child. Exactly one of TypeName/
	// InstanceScenePath must be set.
	InstanceScenePath *string `json:"instance_scene_path,omitempty" jsonschema:"path to another .tscn file, relative to the project root, to instance as the new child (like the editor's \"Instance Child Scene\"); exactly one of type_name/instance_scene_path must be set"`
}

// AddNodeResult confirms a completed node addition.
type AddNodeResult struct {
	// Path is the scene's res://-style path.
	Path string `json:"path"`
	// ParentNodePath is echoed back from the request for confirmation.
	ParentNodePath string `json:"parent_node_path"`
	// Name is echoed back from the request for confirmation — always the
	// name that was actually requested, since a naming collision is
	// rejected rather than silently renamed.
	Name string `json:"name"`
	// Type is the new node's actual Godot class: equal to TypeName in the
	// TypeName case, or the instanced sub-scene's own root class in the
	// InstanceScenePath case (which the caller may not know ahead of time).
	Type string `json:"type"`
}

// AddNode loads an existing .tscn file headlessly, adds one new child node
// (either a bare node of a built-in class, or an instance of another
// project .tscn), and saves the scene back to disk. See SetNodeProperty's
// doc comment (headless.go) for why this genuinely needs Godot and for the
// uid-preservation caveat, both of which apply identically here.
func (c *Client) AddNode(ctx context.Context, params AddNodeParams) (*AddNodeResult, error) {
	absScenePath, err := c.Root.Resolve(params.ScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: add_node: %w", err)
	}
	if filepath.Ext(absScenePath) != ".tscn" {
		return nil, fmt.Errorf("headless: add_node: not a .tscn file: %s", params.ScenePath)
	}
	if params.Name == "" {
		return nil, errors.New("headless: add_node: name is required")
	}
	if (params.TypeName == nil) == (params.InstanceScenePath == nil) {
		return nil, errors.New("headless: add_node: exactly one of type_name, instance_scene_path must be set")
	}

	relScenePath, err := filepath.Rel(c.Root.String(), absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: add_node: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relScenePath)

	// InstanceScenePath goes through the same Root.Resolve trust boundary
	// as ScenePath itself, converted to a res:// path the same way.
	var instanceResPath *string
	if params.InstanceScenePath != nil {
		absInstancePath, err := c.Root.Resolve(*params.InstanceScenePath)
		if err != nil {
			return nil, fmt.Errorf("headless: add_node: %w", err)
		}
		relInstancePath, err := filepath.Rel(c.Root.String(), absInstancePath)
		if err != nil {
			return nil, fmt.Errorf("headless: add_node: computing project-relative instance path: %w", err)
		}
		rp := "res://" + filepath.ToSlash(relInstancePath)
		instanceResPath = &rp
	}

	sceneInfo, err := os.Stat(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: add_node: %w", err)
	}
	originalScene, err := os.ReadFile(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: add_node: %w", err)
	}

	var result struct {
		Type string `json:"type"`
	}
	if err := c.run(ctx, "add_node", struct {
		Path              string  `json:"path"`
		ParentNodePath    string  `json:"parent_node_path"`
		Name              string  `json:"name"`
		TypeName          *string `json:"type_name,omitempty"`
		InstanceScenePath *string `json:"instance_scene_path,omitempty"`
	}{
		Path:              resPath,
		ParentNodePath:    params.ParentNodePath,
		Name:              params.Name,
		TypeName:          params.TypeName,
		InstanceScenePath: instanceResPath,
	}, &result); err != nil {
		return nil, fmt.Errorf("headless: add_node: %w", err)
	}

	updatedScene, err := os.ReadFile(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: add_node: reading saved scene to restore any dropped uid attributes: %w", err)
	}
	if patched, changed := preserveSceneUIDs(string(originalScene), string(updatedScene)); changed {
		if err := os.WriteFile(absScenePath, []byte(patched), sceneInfo.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("headless: add_node: restoring dropped uid attributes: %w", err)
		}
	}

	return &AddNodeResult{
		Path:           resPath,
		ParentNodePath: params.ParentNodePath,
		Name:           params.Name,
		Type:           result.Type,
	}, nil
}

// RemoveNodeParams are the parameters for the remove_node operation.
type RemoveNodeParams struct {
	// ScenePath is the .tscn path, relative to the project root. Validated
	// against Root before use. The scene must already exist.
	ScenePath string `json:"scene_path" jsonschema:"path to an existing .tscn scene file, relative to the project root"`
	// NodePath addresses the node to remove, relative to the scene root,
	// using Godot's own NodePath syntax. Must not be empty: removing the
	// scene's own root node is refused outright.
	NodePath string `json:"node_path" jsonschema:"the node to remove, relative to the scene root, using Godot's own NodePath syntax; must not be empty (removing the scene root itself is refused)"`
}

// RemoveNodeResult confirms a completed node removal.
type RemoveNodeResult struct {
	// Path is the scene's res://-style path.
	Path string `json:"path"`
	// NodePath is echoed back from the request for confirmation.
	NodePath string `json:"node_path"`
	// RemovedType is the removed node's own Godot class, for audit
	// purposes.
	RemovedType string `json:"removed_type"`
	// RemovedNodeCount is the total number of nodes actually removed: the
	// addressed node plus every descendant, since removing a node removes
	// its entire subtree (the same thing that happens if a human deletes
	// it in the editor) — reported so the caller can see the blast radius
	// after the fact.
	RemovedNodeCount int `json:"removed_node_count"`
}

// RemoveNode loads an existing .tscn file headlessly, removes one node (and
// its entire subtree), and saves the scene back to disk. This tool never
// checks or fixes up other nodes' NodePath-typed properties or signal
// connections that may have referenced the removed node — the same
// "cross-reference correctness is not this tool's job" division of
// responsibility already documented for set_script_identity.
func (c *Client) RemoveNode(ctx context.Context, params RemoveNodeParams) (*RemoveNodeResult, error) {
	absScenePath, err := c.Root.Resolve(params.ScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: remove_node: %w", err)
	}
	if filepath.Ext(absScenePath) != ".tscn" {
		return nil, fmt.Errorf("headless: remove_node: not a .tscn file: %s", params.ScenePath)
	}
	if params.NodePath == "" {
		return nil, errors.New("headless: remove_node: node_path is required (removing the scene root is refused)")
	}

	relScenePath, err := filepath.Rel(c.Root.String(), absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: remove_node: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relScenePath)

	sceneInfo, err := os.Stat(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: remove_node: %w", err)
	}
	originalScene, err := os.ReadFile(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: remove_node: %w", err)
	}

	var result struct {
		RemovedType      string `json:"removed_type"`
		RemovedNodeCount int    `json:"removed_node_count"`
	}
	if err := c.run(ctx, "remove_node", struct {
		Path     string `json:"path"`
		NodePath string `json:"node_path"`
	}{Path: resPath, NodePath: params.NodePath}, &result); err != nil {
		return nil, fmt.Errorf("headless: remove_node: %w", err)
	}

	updatedScene, err := os.ReadFile(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: remove_node: reading saved scene to restore any dropped uid attributes: %w", err)
	}
	if patched, changed := preserveSceneUIDs(string(originalScene), string(updatedScene)); changed {
		if err := os.WriteFile(absScenePath, []byte(patched), sceneInfo.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("headless: remove_node: restoring dropped uid attributes: %w", err)
		}
	}

	return &RemoveNodeResult{
		Path:             resPath,
		NodePath:         params.NodePath,
		RemovedType:      result.RemovedType,
		RemovedNodeCount: result.RemovedNodeCount,
	}, nil
}

// ReparentNodeParams are the parameters for the reparent_node operation.
type ReparentNodeParams struct {
	// ScenePath is the .tscn path, relative to the project root. Validated
	// against Root before use. The scene must already exist.
	ScenePath string `json:"scene_path" jsonschema:"path to an existing .tscn scene file, relative to the project root"`
	// NodePath addresses the node to move, relative to the scene root,
	// using Godot's own NodePath syntax. Must not be empty: moving the
	// scene's own root node is refused outright, the same guard
	// remove_node applies.
	NodePath string `json:"node_path" jsonschema:"the node to move, relative to the scene root, using Godot's own NodePath syntax; must not be empty (moving the scene root itself is refused)"`
	// NewParentNodePath addresses the destination parent, relative to the
	// scene root. Empty string means the scene root itself.
	NewParentNodePath string `json:"new_parent_node_path" jsonschema:"the node's new parent, relative to the scene root, using Godot's own NodePath syntax; empty string means the scene root itself"`
	// NewName optionally renames the node as part of the move. Nil keeps
	// the current name. Either way, a name that collides with an existing
	// sibling under the new parent is rejected outright, never silently
	// renamed — the same policy add_node already applies.
	NewName *string `json:"new_name,omitempty" jsonschema:"optionally rename the node as part of the move; nil keeps its current name; rejected if it collides with an existing sibling under new_parent_node_path"`
	// Index optionally sets the node's 0-based child position under the
	// new parent. Nil means Godot's own default placement (appended at
	// the end).
	Index *int `json:"index,omitempty" jsonschema:"optional 0-based child index under the new parent; nil appends at the end"`
}

// ReparentNodeResult confirms a completed node move.
type ReparentNodeResult struct {
	// Path is the scene's res://-style path.
	Path string `json:"path"`
	// NodePath is echoed back from the request for confirmation — the
	// node's address *before* the move, now stale as an address since the
	// node lives elsewhere afterward.
	NodePath string `json:"node_path"`
	// NewParentNodePath is echoed back from the request for confirmation.
	NewParentNodePath string `json:"new_parent_node_path"`
	// PreviousParentNodePath is the node's parent immediately before the
	// move, for audit purposes.
	PreviousParentNodePath string `json:"previous_parent_node_path"`
	// Name is the node's final name after the move — equal to NewName if
	// it was given, otherwise the node's unchanged existing name.
	Name string `json:"name"`
}

// ReparentNode loads an existing .tscn file headlessly, moves one node (and
// its entire subtree, unchanged) to a new parent within the same scene, and
// saves the scene back to disk. Refuses to move the scene root, to move a
// node under itself or one of its own descendants (which would create a
// cycle), or into a name collision with an existing sibling under the new
// parent. This tool never checks or fixes up other nodes' NodePath-typed
// properties or signal connections that may have referenced the moved node
// by its old path — the same "cross-reference correctness is not this
// tool's job" division of responsibility already documented for
// remove_node and set_script_identity.
func (c *Client) ReparentNode(ctx context.Context, params ReparentNodeParams) (*ReparentNodeResult, error) {
	absScenePath, err := c.Root.Resolve(params.ScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: reparent_node: %w", err)
	}
	if filepath.Ext(absScenePath) != ".tscn" {
		return nil, fmt.Errorf("headless: reparent_node: not a .tscn file: %s", params.ScenePath)
	}
	if params.NodePath == "" {
		return nil, errors.New("headless: reparent_node: node_path is required (moving the scene root is refused)")
	}
	if params.NewName != nil && *params.NewName == "" {
		return nil, errors.New("headless: reparent_node: new_name must not be empty")
	}

	relScenePath, err := filepath.Rel(c.Root.String(), absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: reparent_node: computing project-relative path: %w", err)
	}
	resPath := "res://" + filepath.ToSlash(relScenePath)

	sceneInfo, err := os.Stat(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: reparent_node: %w", err)
	}
	originalScene, err := os.ReadFile(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: reparent_node: %w", err)
	}

	var result struct {
		Name                   string `json:"name"`
		PreviousParentNodePath string `json:"previous_parent_node_path"`
	}
	if err := c.run(ctx, "reparent_node", struct {
		Path              string  `json:"path"`
		NodePath          string  `json:"node_path"`
		NewParentNodePath string  `json:"new_parent_node_path"`
		NewName           *string `json:"new_name,omitempty"`
		Index             *int    `json:"index,omitempty"`
	}{
		Path:              resPath,
		NodePath:          params.NodePath,
		NewParentNodePath: params.NewParentNodePath,
		NewName:           params.NewName,
		Index:             params.Index,
	}, &result); err != nil {
		return nil, fmt.Errorf("headless: reparent_node: %w", err)
	}

	updatedScene, err := os.ReadFile(absScenePath)
	if err != nil {
		return nil, fmt.Errorf("headless: reparent_node: reading saved scene to restore any dropped uid attributes: %w", err)
	}
	if patched, changed := preserveSceneUIDs(string(originalScene), string(updatedScene)); changed {
		if err := os.WriteFile(absScenePath, []byte(patched), sceneInfo.Mode().Perm()); err != nil {
			return nil, fmt.Errorf("headless: reparent_node: restoring dropped uid attributes: %w", err)
		}
	}

	return &ReparentNodeResult{
		Path:                   resPath,
		NodePath:               params.NodePath,
		NewParentNodePath:      params.NewParentNodePath,
		PreviousParentNodePath: result.PreviousParentNodePath,
		Name:                   result.Name,
	}, nil
}
