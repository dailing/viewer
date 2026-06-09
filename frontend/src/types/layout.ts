export type SplitDirection = "horizontal" | "vertical";

export type PaneContent = {
  filePath?: string;
  terminalId?: string;
  agentSession?: string;
  diffPath?: string;
  diffCwd?: string;
};

export type LayoutNode =
  | {
      type: "pane";
      id: string;
      filePath?: string;
      terminalId?: string;
      agentSession?: string;
      diffPath?: string;
      diffCwd?: string;
      history?: PaneContent[];
    }
  | {
      type: "split";
      id: string;
      direction: SplitDirection;
      ratio: number;
      first: LayoutNode;
      second: LayoutNode;
    };
