export const blockSize: number;
export const firstBlock: number;
export const lastBlock: number;
export function reservePortBlock(range?: {
  first?: number;
  last?: number;
}): Promise<number>;
export function dashboardPorts(base?: number): {
  api: number;
  dev: number;
  devApi: number;
  gitProvider: number;
  github: number;
  hosted: number;
  hostedBackend: number;
};
export function portBlock(environment?: NodeJS.ProcessEnv): number;
