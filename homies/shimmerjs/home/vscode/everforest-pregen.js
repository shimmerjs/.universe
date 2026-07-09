const fs = require("fs");
const path = require("path");

const ext = process.env.EXT;
const config = JSON.parse(process.env.EF_CONFIG);
const Utils = require(path.join(ext, "dist", "utils")).default;

const data = new Utils().getThemeData(config);
const write = (file, content) =>
  fs.writeFileSync(file, JSON.stringify(content, null, 2));

write(path.join(ext, "themes", "everforest-dark.json"), data.dark);
write(path.join(ext, "themes", "everforest-light.json"), data.light);
write(path.join(ext, ".flag"), "");
