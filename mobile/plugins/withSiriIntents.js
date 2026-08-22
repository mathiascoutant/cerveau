const fs = require('fs');
const path = require('path');

const { withDangerousMod, withXcodeProject } = require('expo/config-plugins');

const SWIFT_FILE = 'RaoulIntents.swift';

/**
 * Ajoute l'intégration Siri (App Intents) au projet iOS.
 *
 * `ios/` est régénéré par prebuild et gitignoré : le fichier Swift ne peut donc
 * pas y être posé à la main. Ce plugin le recopie à chaque génération et
 * l'inscrit comme source de la cible principale.
 *
 * L'intent vit dans la cible de l'app plutôt que dans une extension : même
 * bundle, donc même trousseau, donc aucun access group ni App Group à gérer.
 */
function withSiriIntents(config) {
  // 1. Recopier le source dans le projet généré.
  config = withDangerousMod(config, [
    'ios',
    async (cfg) => {
      const { projectRoot, platformProjectRoot, projectName } = cfg.modRequest;
      const source = path.join(projectRoot, 'plugins', 'ios', SWIFT_FILE);
      const target = path.join(platformProjectRoot, projectName, SWIFT_FILE);

      if (!fs.existsSync(source)) {
        throw new Error(`withSiriIntents : ${source} est introuvable.`);
      }
      fs.copyFileSync(source, target);
      return cfg;
    },
  ]);

  // 2. L'inscrire dans le projet Xcode, sinon il est copié mais jamais compilé.
  config = withXcodeProject(config, (cfg) => {
    const project = cfg.modResults;
    const groupName = cfg.modRequest.projectName;
    const relativePath = `${groupName}/${SWIFT_FILE}`;

    if (project.hasFile(relativePath)) return cfg;

    const groupKey = project.findPBXGroupKey({ name: groupName });
    if (!groupKey) {
      throw new Error(`withSiriIntents : groupe Xcode « ${groupName} » introuvable.`);
    }
    project.addSourceFile(
      relativePath,
      { target: project.getFirstTarget().uuid },
      groupKey,
    );
    return cfg;
  });

  return config;
}

module.exports = withSiriIntents;
