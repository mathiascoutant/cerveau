const fs = require('fs');
const path = require('path');

const { withDangerousMod, withXcodeProject } = require('expo/config-plugins');

const TARGET = 'RaoulWidget';
const SOURCES = ['RaoulWidget.swift'];
const INFO_PLIST = 'Info.plist';

/**
 * Ajoute le widget « Parler à Raoul » au projet iOS.
 *
 * Un widget est une cible Xcode à part entière — une extension d'app avec son
 * propre bundle, son Info.plist et sa signature. `ios/` étant régénéré par
 * prebuild et gitignoré, tout est recréé ici à chaque génération : les sources
 * sont recopiées depuis plugins/ios/, la cible est fabriquée, et le produit est
 * embarqué dans l'app.
 *
 * Le widget n'écoute rien lui-même : iOS réserve le micro aux apps au premier
 * plan. Il ouvre `raoul://listen`, et c'est l'app qui démarre l'écoute.
 */
function withRaoulWidget(config) {
  // 1. Recopier les sources dans le projet généré.
  config = withDangerousMod(config, [
    'ios',
    async (cfg) => {
      const source = path.join(cfg.modRequest.projectRoot, 'plugins', 'ios', TARGET);
      const target = path.join(cfg.modRequest.platformProjectRoot, TARGET);

      if (!fs.existsSync(source)) {
        throw new Error(`withRaoulWidget : ${source} est introuvable.`);
      }
      fs.mkdirSync(target, { recursive: true });
      for (const file of [...SOURCES, INFO_PLIST]) {
        fs.copyFileSync(path.join(source, file), path.join(target, file));
      }
      return cfg;
    },
  ]);

  // 2. Fabriquer la cible et l'inscrire dans le projet.
  config = withXcodeProject(config, (cfg) => {
    const project = cfg.modResults;

    // prebuild peut rejouer le mod sur un projet déjà modifié. Le nom est
    // cherché sous ses deux formes : le writer du paquet `xcode` écrit le
    // commentaire de cible entre guillemets, et pbxTargetByName compare au
    // commentaire brut.
    if (project.pbxTargetByName(TARGET) || project.pbxTargetByName(`"${TARGET}"`)) {
      return cfg;
    }

    const appBundleId = cfg.ios?.bundleIdentifier;
    if (!appBundleId) {
      throw new Error('withRaoulWidget : ios.bundleIdentifier absent de app.json.');
    }
    const widgetBundleId = `${appBundleId}.widget`;

    // addTarget pose la dépendance de build de l'app vers le widget — mais
    // seulement si les sections qui l'accueillent existent déjà, sinon il
    // passe son chemin sans un mot. Un projet Expo n'a ni cible de test ni
    // extension, donc ces deux sections sont absentes : on les crée, faute de
    // quoi rien ne garantit que le widget soit compilé avant d'être embarqué.
    const objects = project.hash.project.objects;
    objects.PBXTargetDependency = objects.PBXTargetDependency || {};
    objects.PBXContainerItemProxy = objects.PBXContainerItemProxy || {};

    // addTarget se charge aussi de la phase « Embed App Extensions » sur la
    // cible principale : sans elle, le widget compilerait sans jamais être
    // livré dans l'app.
    const target = project.addTarget(TARGET, 'app_extension', TARGET, widgetBundleId);

    project.addBuildPhase([], 'PBXSourcesBuildPhase', 'Sources', target.uuid);
    project.addBuildPhase([], 'PBXResourcesBuildPhase', 'Resources', target.uuid);
    project.addBuildPhase([], 'PBXFrameworksBuildPhase', 'Frameworks', target.uuid);

    // Un groupe pour que la cible soit lisible dans Xcode.
    const group = project.addPbxGroup([], TARGET, TARGET);
    const mainGroup = project.getFirstProject().firstProject.mainGroup;
    project.addToPbxGroup(group.uuid, mainGroup);

    // Chemin nu : le groupe porte déjà `path = RaoulWidget`, et Xcode résout
    // ses enfants relativement à lui. Préfixer ici donnerait RaoulWidget/RaoulWidget/.
    for (const file of SOURCES) {
      project.addSourceFile(file, { target: target.uuid }, group.uuid);
    }

    applyBuildSettings(project, {
      INFOPLIST_FILE: `${TARGET}/${INFO_PLIST}`,
      PRODUCT_BUNDLE_IDENTIFIER: widgetBundleId,
      // L'extension est livrée DANS l'app, elle ne s'installe pas seule.
      SKIP_INSTALL: 'YES',
      // Le widget est vendu avec l'app : mêmes versions, sinon l'App Store
      // rejette le paquet pour incohérence de numérotation.
      MARKETING_VERSION: appVersionOf(project),
      CURRENT_PROJECT_VERSION: buildNumberOf(project),
      IPHONEOS_DEPLOYMENT_TARGET: deploymentTargetOf(project),
      DEVELOPMENT_TEAM: developmentTeamOf(project),
      TARGETED_DEVICE_FAMILY: '"1,2"',
      SWIFT_VERSION: '5.0',
      // L'Info.plist est fourni : laisser Xcode en générer un second écraserait
      // la déclaration NSExtension, et le widget n'apparaîtrait nulle part.
      GENERATE_INFOPLIST_FILE: 'NO',
      CODE_SIGN_STYLE: 'Automatic',
      CLANG_ENABLE_MODULES: 'YES',
      SWIFT_EMIT_LOC_STRINGS: 'YES',
      ALWAYS_SEARCH_USER_PATHS: 'NO',
    });

    return cfg;
  });

  return config;
}

/** Applique des réglages aux deux configurations de la cible du widget. */
function applyBuildSettings(project, settings) {
  const configurations = project.pbxXCBuildConfigurationSection();
  const clean = Object.fromEntries(
    Object.entries(settings).filter(([, value]) => value !== undefined && value !== ''),
  );

  for (const key of Object.keys(configurations)) {
    const entry = configurations[key];
    if (typeof entry !== 'object' || !entry.buildSettings) continue;
    // addTarget écrit PRODUCT_NAME entre guillemets : c'est ce qui identifie
    // les deux configurations qui viennent d'être créées pour le widget.
    if (unquote(entry.buildSettings.PRODUCT_NAME) !== TARGET) continue;
    entry.buildSettings = { ...entry.buildSettings, ...clean };
  }
}

/** Lit un réglage sur la cible principale, pour aligner le widget dessus. */
function appSetting(project, name) {
  const app = project.getFirstTarget();
  const configurations = project.pbxXCBuildConfigurationSection();
  const listKey = project.pbxNativeTargetSection()[app.uuid]?.buildConfigurationList;
  const list = project.pbxXCConfigurationList()[listKey];

  for (const ref of list?.buildConfigurations ?? []) {
    const value = configurations[ref.value]?.buildSettings?.[name];
    if (value !== undefined) return value;
  }
  return undefined;
}

const appVersionOf = (p) => appSetting(p, 'MARKETING_VERSION');
const buildNumberOf = (p) => appSetting(p, 'CURRENT_PROJECT_VERSION');
const deploymentTargetOf = (p) => appSetting(p, 'IPHONEOS_DEPLOYMENT_TARGET');
const developmentTeamOf = (p) => appSetting(p, 'DEVELOPMENT_TEAM');

function unquote(value) {
  return typeof value === 'string' ? value.replace(/^"(.*)"$/, '$1') : value;
}

module.exports = withRaoulWidget;
