# NDecrypt sidecar. Built and run as part of the ROM Relay stack; it is
# reachable only on the internal compose network (never published to the host).
# Build context is the repo root (not this directory) because ndecrypt.csproj
# references the third_party/ndecrypt submodule by relative path.
FROM mcr.microsoft.com/dotnet/sdk:10.0 AS build
WORKDIR /src
COPY third_party/ndecrypt third_party/ndecrypt
COPY sidecar/ndecrypt sidecar/ndecrypt
WORKDIR /src/sidecar/ndecrypt
RUN dotnet restore
RUN dotnet publish ndecrypt.csproj -c Release -o /app --no-restore

FROM mcr.microsoft.com/dotnet/aspnet:10.0
WORKDIR /app
COPY --from=build /app .
ENV ASPNETCORE_URLS=http://0.0.0.0:8082
ENV ASPNETCORE_ENVIRONMENT=Production
EXPOSE 8082
ENTRYPOINT ["dotnet", "ndecrypt-sidecar.dll"]
