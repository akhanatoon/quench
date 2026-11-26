import fs from "node:fs";

const content = fs.readFileSync("src/price.ts", "utf8");
if (content.includes('"not-a-number"')) {
  console.error("src/price.ts(2,9): error TS2322: Type 'string' is not assignable to type 'number'.");
  process.exit(2);
}
