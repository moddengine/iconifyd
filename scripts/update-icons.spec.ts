import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const workflow = readFileSync(
  new URL("../.github/workflows/update-icons.yml", import.meta.url),
  "utf8",
);
const script = workflow.split("        run: |\n")[1].replace(/^ {10}/gm, "");

function update(latest: string, failPush = false) {
  return execFileSync(
    "bash",
    [
      "-e",
      "-c",
      `
        node() { echo 2.2.544; }
        npm() {
          if [ "$1" = view ]; then echo "$LATEST";
          else echo "npm $*"; fi
        }
        git() {
          echo "git $*"
          if [ "$1" = push ] && [ "$FAIL_PUSH" = true ]; then return 1; fi
        }
        gh() { echo "gh $*"; }
        ${script}
      `,
    ],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        LATEST: latest,
        FAIL_PUSH: String(failPush),
        GITHUB_STEP_SUMMARY: "/dev/null",
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
}

describe("monthly icon database update", () => {
  it("does not commit or release an unchanged version", () => {
    expect(update("2.2.544")).toBe("");
  });

  it("pins the new version and dispatches release checks after pushing", () => {
    const output = update("2.2.545");
    expect(output).toContain(
      "npm install --save-dev --save-exact --package-lock-only --ignore-scripts --no-audit --no-fund @iconify/json@2.2.545",
    );
    expect(output).toContain("git add package.json package-lock.json");
    expect(output).toContain(
      "git commit -m Update Iconify database to 2.2.545",
    );
    expect(output).toContain(
      "git push origin HEAD:main\ngh workflow run release.yml --ref main\n",
    );
  });

  it("does not dispatch a release when pushing fails", () => {
    try {
      update("2.2.545", true);
      expect.fail("push should fail");
    } catch (error) {
      const failure = error as Error & { status: number; stdout: string };
      expect(failure.status).toBe(1);
      expect(failure.stdout).not.toContain("gh workflow run");
    }
  });
});
