# Grounding: bloom

bloomfx.dev/bloom is a layout, compositing, and animation engine for Bubble Tea programs. It requires Go 1.27+, charm.land/bubbletea/v2, and charm.land/lipgloss/v2. Pre-1.0: expect breaking changes until v1.0.0, so pin versions.

## Model

Each frame, View builds a tree of nodes from model state. bloom lays out the tree, draws nodes into cells, composites, runs effects, records geometry, and returns a tea.View with an OnMouse handler. Nodes: Row, Column, Fixed, Fill, Percent, Min/Max, Collapsible slots; Overlay and Layer for dialogs, toasts, popovers with backdrops and modal input; Text, Box, Viewport, List for content.

## What a multiplexer client uses

- Splits: Row and Column with Fixed, Fill, Min, and Collapsible. Drag borders via on.Drag. Click-to-focus via on.Down and focus scopes.
- Zoom, popups, command palette: layers with backdrops.
- Frame records where every node went, so mapping pane rectangles to RESIZE frames is direct.
- Viewport draws only the lines in sight. Strings that change every frame are re-parsed every frame, so live pane content wants a cheaper path than styled strings per frame.
- MouseMode is chosen per frame from what the tree needs. Selectable nodes give in-app text selection with copy over OSC 52.

## Performance context

From bloom's own docs (measured, reproducible via go test -bench in the bloom repo): an unchanged 200x60 frame costs about 0.02ms, a mostly-changed frame about 0.3ms, a dialog animating over a dimmed backdrop about 0.4ms. Bubble Tea caps at 120fps.

## Open question for the design

The cell-level draw path (canvas.Surface, Drawable) is listed in bloom's docs index but its page was not read in full this session. Candidates may design the pane node against a cell path with a styled-strings fallback, and must flag the assumption.
