{
  asList(name, data, parameters):: {
    apiVersion: 'v1',
    kind: 'Template',
    metadata: {
      name: name,
    },
    objects: [data[k] for k in std.objectFields(data)],
    parameters: parameters,
  },

  withImage(_config):: {
    local setImage(object) =
      if object.kind == 'StatefulSet' then {
        spec+: {
          template+: {
            spec+: {
              containers: [
                c {
                  image: if c.name == 'telemeter-server' then '${IMAGE}:${IMAGE_TAG}' else c.image,
                }
                for c in super.containers
              ],
            },
          },
        },
      }
      else {},
    objects: [
      o + setImage(o)
      for o in super.objects
    ],
    parameters+: [
      { name: 'IMAGE', value: _config.imageRepos.telemeterServer },
      { name: 'IMAGE_TAG', value: _config.versions.telemeterServer },
    ],
  },

  withAuthorizeURL(_config):: {
    local setAuthorizeURL(object) =
      if object.kind == 'StatefulSet' then {
        spec+: {
          template+: {
            spec+: {
              containers: [
                c {
                  command: [
                    if std.startsWith(c, '--authorize=') then '--authorize=${AUTHORIZE_URL}' else c
                    for c in super.command
                  ],
                }
                for c in super.containers
              ],
            },
          },
        },
      }
      else {},
    objects: [
      o + setAuthorizeURL(o)
      for o in super.objects
    ],
    parameters+: [
      { name: 'AUTHORIZE_URL', value: _config.telemeterServer.authorizeURL },
    ],
  },
}
