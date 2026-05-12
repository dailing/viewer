export type SplitDirection = "horizontal" | "vertical";

export type LayoutNode =
  | {
      type: "pane";
      id: string;
      filePath?: string;
      terminalId?: string;
      agentSession?: string;
      codexSessionId?: string;
      hermesSessionId?: string;
      diffPath?: string;
      diffCwd?: string;
    }
  | {
      type: "split";
      id: string;
      direction: SplitDirection;
      ratio: number;
      first: LayoutNode;
      second: LayoutNode;
    };
